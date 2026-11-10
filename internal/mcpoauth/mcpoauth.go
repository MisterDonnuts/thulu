package mcpoauth

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/netguard"
)

const FlowTTL = 15 * time.Minute

const refreshSkew = 2 * time.Minute

const callTimeout = 20 * time.Second

var (
	// Unknown, used, expired and wrong-browser states share one error on purpose.
	ErrState         error = apperr.Invalid("mcpoauth.errors.state")
	ErrReconnect     error = apperr.Precondition("mcpoauth.errors.reconnect_required")
	ErrWrongResource error = apperr.Forbidden("mcpoauth.errors.wrong_resource")
	ErrNotOAuth      error = apperr.Invalid("mcpoauth.errors.not_oauth")
)

type CredentialStore interface {
	Get(ctx context.Context, workspaceID, id string) (*credentials.Credential, error)
	List(ctx context.Context, workspaceID string) ([]*credentials.Credential, error)
	Payload(ctx context.Context, workspaceID, id string) (map[string]string, error)
	Create(ctx context.Context, workspaceID string, in credentials.Input, createdBy string) (*credentials.Credential, error)
	SetGrants(ctx context.Context, workspaceID, id string, grants []credentials.Grant) error
	SetPayload(ctx context.Context, workspaceID, id string, payload map[string]string, in credentials.Introspection) error
}

type Service struct {
	db    *sql.DB
	creds CredentialStore
	// Internal policy, only for the operator-chosen server origin (may be private); no redirects
	// off that origin.
	server *http.Client
	// Egress policy for every URL learned from responses, so a server's answers cannot aim the
	// daemon at its own network.
	client     *http.Client
	now        func() time.Time
	endpointOK func(raw string) bool
	refreshes  singleflight.Group
}

func New(db *sql.DB, creds CredentialStore) (*Service, error) {
	if err := migrateFlows(context.Background(), db); err != nil {
		return nil, err
	}
	return &Service{
		db: db, creds: creds,
		server: &http.Client{
			Transport: netguard.NewInternal().Transport(), Timeout: callTimeout,
			CheckRedirect: sameOriginRedirect,
		},
		client:     netguard.NewEgress().Client(callTimeout),
		now:        time.Now,
		endpointOK: secureEndpoint,
	}, nil
}

func (s *Service) WithHTTPClient(c *http.Client) *Service {
	s.server, s.client = c, c
	return s
}

func (s *Service) clientFor(serverURL, target string) *http.Client {
	if o := origin(serverURL); o != "" && o == origin(target) {
		return s.server
	}
	return s.client
}

func sameOriginRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 || origin(req.URL.String()) != origin(via[0].URL.String()) {
		return http.ErrUseLastResponse
	}
	return nil
}

func (s *Service) WithClock(now func() time.Time) *Service {
	s.now = now
	return s
}
