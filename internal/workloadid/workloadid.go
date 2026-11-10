package workloadid

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// Providers compare this byte for byte with each token's iss; it must not change once a federation
// issuer is registered.
const IssuerEnv = "OCTOPUS_WORKLOAD_IDENTITY_ISSUER"

// Anthropic caches a fetched key set for up to a minute and asks issuers to publish a new key well
// ahead of first use.
const RotationLead = 15 * time.Minute

const RetiredKeyGrace = MaxTokenTTL + time.Hour

const MaxTokenTTL = 24 * time.Hour

// Bounds staleness so a rotation made by another replica reaches this one.
const keyCacheTTL = time.Minute

type Key struct {
	KID         string
	Private     []byte
	CreatedAt   time.Time
	ActivatesAt time.Time
	RetiredAt   time.Time
}

type Store interface {
	WorkloadIdentityKeys(ctx context.Context) ([]Key, error)
	// Must insert k and set retired_at = retireAt on every unretired key in the same transaction.
	AddWorkloadIdentityKey(ctx context.Context, k Key, retireAt time.Time) error
}

type Issuer struct {
	store  Store
	getenv func(string) string
	now    func() time.Time

	mu       sync.Mutex
	keys     []parsedKey
	loadedAt time.Time
}

type parsedKey struct {
	Key
	priv *ecdsa.PrivateKey
}

func New(store Store, getenv func(string) string) *Issuer {
	return &Issuer{store: store, getenv: getenv, now: time.Now}
}

func (i *Issuer) URL() string {
	if i == nil {
		return ""
	}
	if v := strings.TrimSpace(i.getenv(IssuerEnv)); v != "" {
		return strings.TrimRight(v, "/")
	}
	if d := strings.TrimSpace(i.getenv("OCTOPUS_DOMAIN")); d != "" {
		return "https://" + strings.TrimRight(d, "/")
	}
	return ""
}

func (i *Issuer) Ready() error {
	if i == nil {
		return apperr.Precondition("workloadid.errors.not_configured", "env", IssuerEnv)
	}
	iss := i.URL()
	if iss == "" {
		return apperr.Precondition("workloadid.errors.not_configured", "env", IssuerEnv)
	}
	u, err := url.Parse(iss)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return apperr.Invalid("workloadid.errors.issuer_not_https", "env", IssuerEnv, "value", iss)
	}
	return nil
}

type RunClaims struct {
	WorkspaceID string
	TaskID      string
	ChatID      string
	RunID       string
	Audience    string
	TTL         time.Duration
}

// Federation trust rules match on this sub format; changing it breaks registered rules.
func Subject(c RunClaims) string {
	sub := SubjectPrefix(c.WorkspaceID)
	switch {
	case c.TaskID != "":
		return sub + "task:" + c.TaskID
	case c.ChatID != "":
		return sub + "chat:" + c.ChatID
	default:
		return sub + "run:" + c.RunID
	}
}

func SubjectPrefix(workspaceID string) string {
	//i18n:ignore claim value a federation rule matches, not a message
	return "octopus:workspace:" + workspaceID + ":"
}

// No jti: the CLI re-reads the same token on every exchange, and the provider treats a token with a
// jti as single-use.
func (i *Issuer) Mint(ctx context.Context, c RunClaims) (string, error) {
	if err := i.Ready(); err != nil {
		return "", err
	}
	if c.WorkspaceID == "" || c.Audience == "" {
		return "", apperr.Invalid("workloadid.errors.claims_incomplete")
	}
	k, err := i.signingKey(ctx)
	if err != nil {
		return "", err
	}
	ttl := c.TTL
	if ttl <= 0 || ttl > MaxTokenTTL {
		ttl = MaxTokenTTL
	}
	now := i.now().UTC().Truncate(time.Second)
	claims := map[string]any{
		"iss":          i.URL(),
		"sub":          Subject(c),
		"aud":          c.Audience,
		"iat":          now.Unix(),
		"nbf":          now.Unix(),
		"exp":          now.Add(ttl).Unix(),
		"workspace_id": c.WorkspaceID,
	}
	for name, v := range map[string]string{"task_id": c.TaskID, "chat_id": c.ChatID, "run_id": c.RunID} {
		if v != "" {
			claims[name] = v
		}
	}
	return sign(k, claims)
}

func sign(k parsedKey, claims map[string]any) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "ES256", "typ": "JWT", "kid": k.KID})
	if err != nil {
		return "", err
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	input := enc.EncodeToString(header) + "." + enc.EncodeToString(body)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, k.priv, digest[:])
	if err != nil {
		return "", apperr.Wrap(err, "workloadid.errors.sign")
	}
	// JWS ES256 is r || s, each left-padded to 32 bytes (RFC 7518 §3.4), not ASN.1.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + enc.EncodeToString(sig), nil
}

type JWK struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
}

type JWKS struct {
	Keys []JWK `json:"keys"`
}

func (i *Issuer) JWKS(ctx context.Context) (JWKS, error) {
	keys, err := i.loadKeys(ctx, true)
	if err != nil {
		return JWKS{}, err
	}
	now := i.now()
	out := JWKS{Keys: []JWK{}}
	for _, k := range keys {
		if !k.RetiredAt.IsZero() && now.Sub(k.RetiredAt) > RetiredKeyGrace {
			continue
		}
		out.Keys = append(out.Keys, publicJWK(k))
	}
	return out, nil
}

func publicJWK(k parsedKey) JWK {
	j := thumbprintFields(&k.priv.PublicKey)
	j.Kid = k.KID
	j.Use = "sig"
	j.Alg = "ES256"
	return j
}

func thumbprintFields(pub *ecdsa.PublicKey) JWK {
	enc := base64.RawURLEncoding
	pt, err := pub.Bytes()
	if err != nil || len(pt) != 65 {
		return JWK{Kty: "EC", Crv: "P-256"}
	}
	return JWK{Kty: "EC", Crv: "P-256", X: enc.EncodeToString(pt[1:33]), Y: enc.EncodeToString(pt[33:])}
}

func keyID(pub *ecdsa.PublicKey) string {
	j := thumbprintFields(pub)
	// Members in lexicographic order, no whitespace, as RFC 7638 §3 requires.
	canon := `{"crv":"` + j.Crv + `","kty":"` + j.Kty + `","x":"` + j.X + `","y":"` + j.Y + `"}`
	sum := sha256.Sum256([]byte(canon))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type Discovery struct {
	Issuer                           string   `json:"issuer"`
	JWKSURI                          string   `json:"jwks_uri"`
	IDTokenSigningAlgValuesSupported []string `json:"id_token_signing_alg_values_supported"`
	ResponseTypesSupported           []string `json:"response_types_supported"`
	SubjectTypesSupported            []string `json:"subject_types_supported"`
	ClaimsSupported                  []string `json:"claims_supported"`
}

const (
	DiscoveryPath = "/.well-known/openid-configuration"
	JWKSPath      = "/.well-known/jwks.json"
)

func (i *Issuer) Discovery() (Discovery, error) {
	if err := i.Ready(); err != nil {
		return Discovery{}, err
	}
	iss := i.URL()
	return Discovery{
		Issuer:                           iss,
		JWKSURI:                          iss + JWKSPath,
		IDTokenSigningAlgValuesSupported: []string{"ES256"},
		ResponseTypesSupported:           []string{"id_token"},
		SubjectTypesSupported:            []string{"public"},
		ClaimsSupported:                  []string{"iss", "sub", "aud", "iat", "nbf", "exp", "workspace_id", "task_id", "chat_id", "run_id"},
	}, nil
}

type KeyInfo struct {
	KID         string    `json:"kid"`
	CreatedAt   time.Time `json:"created_at"`
	ActivatesAt time.Time `json:"activates_at"`
	RetiredAt   time.Time `json:"retired_at,omitzero"`
	State       string    `json:"state"`
}

func (i *Issuer) Keys(ctx context.Context) ([]KeyInfo, error) {
	keys, err := i.loadKeys(ctx, true)
	if err != nil {
		return nil, err
	}
	now := i.now()
	signing, _ := current(keys, now)
	out := make([]KeyInfo, 0, len(keys))
	for _, k := range keys {
		info := KeyInfo{KID: k.KID, CreatedAt: k.CreatedAt, ActivatesAt: k.ActivatesAt, RetiredAt: k.RetiredAt}
		switch {
		case k.KID == signing.KID:
			info.State = "signing"
		case k.ActivatesAt.After(now):
			info.State = "pending"
		default:
			info.State = "retired"
		}
		out = append(out, info)
	}
	return out, nil
}

func (i *Issuer) Rotate(ctx context.Context) (KeyInfo, error) {
	activates := i.now().UTC().Add(RotationLead)
	k, err := i.addKey(ctx, activates, activates)
	if err != nil {
		return KeyInfo{}, err
	}
	return KeyInfo{KID: k.KID, CreatedAt: k.CreatedAt, ActivatesAt: k.ActivatesAt, State: "pending"}, nil
}

func (i *Issuer) signingKey(ctx context.Context) (parsedKey, error) {
	keys, err := i.loadKeys(ctx, true)
	if err != nil {
		return parsedKey{}, err
	}
	k, ok := current(keys, i.now())
	if !ok {
		return parsedKey{}, apperr.New("workloadid.errors.no_signing_key")
	}
	return k, nil
}

func current(keys []parsedKey, now time.Time) (parsedKey, bool) {
	var best parsedKey
	found := false
	for _, k := range keys {
		if k.ActivatesAt.After(now) {
			continue
		}
		if !found || k.ActivatesAt.After(best.ActivatesAt) {
			best, found = k, true
		}
	}
	return best, found
}

func (i *Issuer) loadKeys(ctx context.Context, ensure bool) ([]parsedKey, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.keys != nil && i.now().Sub(i.loadedAt) < keyCacheTTL {
		return i.keys, nil
	}
	if err := i.reloadLocked(ctx); err != nil {
		return nil, err
	}
	if len(i.keys) == 0 && ensure {
		now := i.now().UTC()
		if _, err := i.addKeyLocked(ctx, now, now); err != nil {
			return nil, err
		}
	}
	return i.keys, nil
}

func (i *Issuer) reloadLocked(ctx context.Context) error {
	stored, err := i.store.WorkloadIdentityKeys(ctx)
	if err != nil {
		return apperr.Wrap(err, "workloadid.errors.load_keys")
	}
	keys := make([]parsedKey, 0, len(stored))
	for _, k := range stored {
		priv, err := x509.ParsePKCS8PrivateKey(k.Private)
		if err != nil {
			return apperr.Wrap(err, "workloadid.errors.parse_key", "kid", k.KID)
		}
		ec, ok := priv.(*ecdsa.PrivateKey)
		if !ok || ec.Curve != elliptic.P256() {
			return apperr.New("workloadid.errors.parse_key", "kid", k.KID)
		}
		keys = append(keys, parsedKey{Key: k, priv: ec})
	}
	sort.SliceStable(keys, func(a, b int) bool { return keys[a].ActivatesAt.After(keys[b].ActivatesAt) })
	i.keys = keys
	i.loadedAt = i.now()
	return nil
}

func (i *Issuer) addKey(ctx context.Context, activates, retireOthers time.Time) (Key, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if err := i.reloadLocked(ctx); err != nil {
		return Key{}, err
	}
	return i.addKeyLocked(ctx, activates, retireOthers)
}

func (i *Issuer) addKeyLocked(ctx context.Context, activates, retireOthers time.Time) (Key, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Key{}, apperr.Wrap(err, "workloadid.errors.generate_key")
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return Key{}, apperr.Wrap(err, "workloadid.errors.generate_key")
	}
	k := Key{
		KID:         keyID(&priv.PublicKey),
		Private:     der,
		CreatedAt:   i.now().UTC(),
		ActivatesAt: activates,
	}
	if err := i.store.AddWorkloadIdentityKey(ctx, k, retireOthers); err != nil {
		return Key{}, apperr.Wrap(err, "workloadid.errors.store_key")
	}
	if err := i.reloadLocked(ctx); err != nil {
		return Key{}, err
	}
	return k, nil
}
