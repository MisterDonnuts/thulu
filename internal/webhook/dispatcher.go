package webhook

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"

	"github.com/MisterDonnuts/thulu/internal/logx"
	"github.com/MisterDonnuts/thulu/internal/netguard"
)

const maxOutboxAttempts = 5

type Dispatcher struct {
	store  *Store
	client *http.Client
	nudge  chan struct{}
	batch  int
}

// Endpoint URLs are admin-typed and delivered from inside the deployment, so netguard refuses
// loopback, private and metadata addresses.
// Internal endpoints need their range in OCTOPUS_EGRESS_ALLOW_PRIVATE_CIDRS.
func NewDispatcher(ctx context.Context, store *Store) *Dispatcher {
	d := &Dispatcher{
		store:  store,
		client: netguard.NewEgress().Client(10 * time.Second),
		nudge:  make(chan struct{}, 1),
		batch:  50,
	}
	go d.loop(ctx)
	return d
}

func (d *Dispatcher) loop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		for {
			n, err := d.ProcessDue(ctx)
			if err != nil {
				log.Printf("[webhook] %s", i18n.Log("webhook.log.process_outbox_failed", "err", err))
				break
			}
			if n == 0 {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-d.nudge:
		}
	}
}

func (d *Dispatcher) Emit(ctx context.Context, workspaceID string, event EventType, data any) {
	payload := Payload{
		ID:          newID(),
		Event:       event,
		WorkspaceID: workspaceID,
		OccurredAt:  time.Now().UTC(),
		Data:        data,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("[webhook] %s", i18n.Log("webhook.log.marshal_payload_failed", "err", err))
		return
	}

	endpoints, err := d.store.ListEndpoints(ctx, workspaceID)
	if err != nil {
		log.Printf("[webhook] %s", i18n.Log("webhook.log.list_endpoints_failed", "workspace", workspaceID, "err", err))
		return
	}

	enqueued := false
	for i := range endpoints {
		if !endpoints[i].Active || !matchesEvent(endpoints[i].Events, event) {
			continue
		}
		if err := d.store.enqueueOutbox(ctx, endpoints[i].ID, payload, body); err != nil {
			log.Printf("[webhook] %s", i18n.Log("webhook.log.enqueue_failed", "endpoint", endpoints[i].ID, "err", err))
			continue
		}
		enqueued = true
	}
	if enqueued {
		d.signal()
	}
}

func (d *Dispatcher) signal() {
	select {
	case d.nudge <- struct{}{}:
	default:
	}
}

func (d *Dispatcher) DeliverTest(ctx context.Context, endpointID, workspaceID string, data any) (map[string]any, error) {
	ep, err := d.store.GetEndpoint(ctx, endpointID, workspaceID)
	if err != nil {
		return nil, apperr.WrapKind(apperr.KindNotFound, err, "webhook.errors.endpoint_not_found")
	}
	if !ep.Active {
		return nil, apperr.Conflict("webhook.errors.endpoint_inactive")
	}

	payload := Payload{
		ID:          newID(),
		Event:       "test",
		WorkspaceID: workspaceID,
		OccurredAt:  time.Now().UTC(),
		Data:        data,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, apperr.Wrap(err, "webhook.errors.marshal_payload")
	}
	if _, err := url.Parse(ep.URL); err != nil {
		return nil, apperr.Wrap(err, "webhook.errors.build_request")
	}

	start := time.Now()
	statusCode, deliveryErr := d.deliverOnce(ctx, ep, body, string(payload.Event), payload.ID)
	durationMs := time.Since(start).Milliseconds()
	delivered := deliveryErr == ""
	d.store.recordDelivery(ctx, endpointID, "", payload, statusCode, 1, durationMs, deliveryErr, delivered)

	result := map[string]any{
		"delivery_id": payload.ID,
		"duration_ms": durationMs,
		"success":     delivered,
	}
	if statusCode != 0 {
		result["status_code"] = statusCode
	} else {
		result["error"] = deliveryErr
	}
	return result, nil
}

func (d *Dispatcher) ProcessDue(ctx context.Context) (int, error) {
	rows, err := d.store.claimDueDeliveries(ctx, d.batch)
	if err != nil {
		return 0, err
	}
	for i := range rows {
		d.attemptDelivery(ctx, rows[i])
	}
	return len(rows), nil
}

func (d *Dispatcher) attemptDelivery(ctx context.Context, row outboxRow) {
	ep, err := d.store.getEndpointByID(ctx, row.EndpointID)
	if err != nil || ep == nil || !ep.Active {
		logx.Warn(d.store.finalizeOutbox(ctx, row.ID, statusFailed, row.Attempts), "webhook.log.park_orphaned", "id", row.ID)
		return
	}

	start := time.Now()
	statusCode, deliveryErr := d.deliverOnce(ctx, ep, row.Body, row.Event, row.Payload.ID)
	durationMs := time.Since(start).Milliseconds()
	delivered := deliveryErr == ""
	attempt := row.Attempts + 1

	d.store.recordDelivery(ctx, row.EndpointID, row.ID, row.Payload, statusCode, attempt, durationMs, deliveryErr, delivered)

	switch {
	case delivered:
		logx.Warn(d.store.finalizeOutbox(ctx, row.ID, statusDelivered, attempt), "webhook.log.mark_delivered", "id", row.ID)
	case attempt >= maxOutboxAttempts:
		logx.Warn(d.store.finalizeOutbox(ctx, row.ID, statusFailed, attempt), "webhook.log.park_dead_letter", "id", row.ID)
	default:
		backoff := time.Duration(1<<uint(attempt)) * time.Second
		logx.Warn(d.store.rescheduleOutbox(ctx, row.ID, attempt, time.Now().Add(backoff)), "webhook.log.reschedule", "id", row.ID)
	}
}

func (d *Dispatcher) deliverOnce(ctx context.Context, ep *Endpoint, body []byte, event, deliveryID string) (statusCode int, deliveryErr string) {
	key, err := ep.signingKey()
	if err != nil {
		return 0, err.Error()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.URL, bytes.NewReader(body))
	if err != nil {
		return 0, err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Octopus-Event", event)
	req.Header.Set("X-Octopus-Delivery", deliveryID)
	if key != nil {
		req.Header.Set("X-Octopus-Signature", "sha256="+signPayload(body, key))
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, deliveryFailure(ep.URL, err)
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, ""
}

// WithoutCancel: the event must be persisted even after the originating request is gone; the
// timeout bounds a stuck write.
func EmitBackground(ctx context.Context, d *Dispatcher, workspaceID string, event EventType, data any) {
	if d == nil {
		return
	}
	go func() {
		emitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), emitTimeout)
		defer cancel()
		d.Emit(emitCtx, workspaceID, event, data)
	}()
}

const emitTimeout = 10 * time.Second

func (d *Dispatcher) Retry(ctx context.Context, deliveryID, endpointID, workspaceID string) error {
	if err := d.store.requeueDelivery(ctx, deliveryID, endpointID, workspaceID); err != nil {
		return err
	}
	d.signal()
	return nil
}

func deliveryFailure(endpointURL string, err error) string {
	host := endpointURL
	if u, perr := url.Parse(endpointURL); perr == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	var dnsErr *net.DNSError
	var netErr net.Error
	var unknownCA x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var tlsRecord tls.RecordHeaderError
	switch {
	case errors.Is(err, netguard.ErrBlocked):
		return i18n.Text("webhook.delivery.blocked", "host", host)
	case errors.Is(err, netguard.ErrUnresolved), errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return i18n.Text("webhook.delivery.no_such_host", "host", host)
	case errors.As(err, &dnsErr):
		// Must precede the timeout and unreachable cases, which would otherwise blame the endpoint
		// for the daemon's own DNS.
		return i18n.Text("webhook.delivery.dns_failed", "host", host)
	case errors.Is(err, syscall.ECONNREFUSED):
		return i18n.Text("webhook.delivery.refused", "host", host)
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		return i18n.Text("webhook.delivery.unreachable", "host", host)
	case errors.As(err, &unknownCA), errors.As(err, &hostname), errors.As(err, &invalid), errors.As(err, &tlsRecord):
		return i18n.Text("webhook.delivery.tls", "host", host)
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return i18n.Text("webhook.delivery.timeout", "host", host)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		err = urlErr.Err
	}
	return i18n.Text("webhook.delivery.failed", "host", host, "err", err.Error())
}

var failureMessages = sync.OnceValue(func() *i18n.Retranslator {
	return i18n.NewRetranslator(i18n.Default(), func(key string) bool {
		return strings.HasPrefix(key, "webhook.delivery.") //i18n:ignore a catalog key prefix
	})
})

func FailureText(recorded, locale string) string {
	text, _ := failureMessages().Translate(recorded, locale)
	return text
}
