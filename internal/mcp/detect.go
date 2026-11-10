package mcp

import (
	"context"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/netguard"
)

type Detection struct {
	Transport        string `json:"transport"`
	HTTPStatus       int    `json:"http_status"`
	SSEStatus        int    `json:"sse_status"`
	AuthRequired     bool   `json:"auth_required"`
	OAuth            bool   `json:"oauth"`
	ResourceMetadata string `json:"resource_metadata,omitempty"`
}

// The first MCP revision with Streamable HTTP.
const detectProtocolVersion = "2025-03-26"

const detectInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` +
	detectProtocolVersion + `","capabilities":{},"clientInfo":{"name":"octopus-probe","version":"1"}}}`

type Detector struct {
	client *http.Client
}

// Internal network policy: the URL comes from an admin-only form, so private addresses are allowed
// and metadata endpoints refused.
func NewDetector() *Detector {
	tr := netguard.NewInternal().Transport()
	tr.DisableKeepAlives = true
	return &Detector{client: &http.Client{Transport: tr, Timeout: 10 * time.Second}}
}

func NewDetectorWithClient(c *http.Client) *Detector { return &Detector{client: c} }

func (d *Detector) Detect(ctx context.Context, rawURL string) (Detection, error) {
	var out Detection
	resp, err := d.do(ctx, http.MethodPost, rawURL, strings.NewReader(detectInitialize), map[string]string{
		"Content-Type":         "application/json",
		"Accept":               "application/json, text/event-stream", //i18n:ignore HTTP header and media types
		"Mcp-Protocol-Version": detectProtocolVersion,
	})
	if err != nil {
		return out, err
	}
	out.HTTPStatus = resp.status
	out.noteAuth(resp)
	if resp.status/100 == 2 && (resp.isMediaType("application/json") || resp.isMediaType("text/event-stream")) {
		out.Transport = TransportHTTP
		return out, nil
	}

	sseHeaders := map[string]string{"Accept": "text/event-stream"} //i18n:ignore HTTP header and media type
	resp, err = d.do(ctx, http.MethodGet, rawURL, http.NoBody, sseHeaders)
	if err != nil {
		return out, err
	}
	out.SSEStatus = resp.status
	out.noteAuth(resp)
	if resp.status == http.StatusOK && resp.isMediaType("text/event-stream") {
		out.Transport = TransportSSE
	}
	return out, nil
}

type answer struct {
	status int
	header http.Header
}

func (d *Detector) do(ctx context.Context, method, rawURL string, body io.Reader, hdr map[string]string) (answer, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, detectMethod(method), rawURL, body)
	if err != nil {
		return answer{}, apperr.WrapKind(apperr.KindInvalid, err, "mcp.errors.detect_url", "url", rawURL)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return answer{}, apperr.Wrap(err, "mcp.errors.detect_unreachable", "url", rawURL)
	}
	// Keep-alives are off, so closing the body unread drops the connection, SSE streams included.
	resp.Body.Close()
	return answer{status: resp.StatusCode, header: resp.Header}, nil
}

func detectMethod(m string) string {
	if m == http.MethodPost {
		return http.MethodPost
	}
	return http.MethodGet
}

var resourceMetadataRe = regexp.MustCompile(`resource_metadata\s*=\s*"([^"]+)"`)

func (d *Detection) noteAuth(a answer) {
	if a.status != http.StatusUnauthorized {
		return
	}
	d.AuthRequired = true
	for _, v := range a.header.Values("WWW-Authenticate") {
		if m := resourceMetadataRe.FindStringSubmatch(v); m != nil {
			d.OAuth, d.ResourceMetadata = true, m[1]
			return
		}
	}
}

func (a answer) isMediaType(want string) bool {
	mt, _, err := mime.ParseMediaType(a.header.Get("Content-Type"))
	return err == nil && mt == want
}
