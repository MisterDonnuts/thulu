package credentials

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/i18n"
)

type awsProvider struct{}

// Vars only so tests can point them at httptest; nothing a credential carries may set them (SSRF).
var (
	awsSTSEndpoint = "https://sts.amazonaws.com/"
	awsIAMEndpoint = "https://iam.amazonaws.com/"
)

func (p *awsProvider) Spec() ProviderSpec { return p.SpecIn(context.Background()) }

func (p *awsProvider) SpecIn(ctx context.Context) ProviderSpec {
	return ProviderSpec{
		ID:    "aws",
		Label: i18n.TCtx(ctx, "credentials.labels.provider_aws"),
		Help:  i18n.TCtx(ctx, "credentials.hints.provider_aws"),
		Kinds: []KindSpec{{
			ID:    "aws_keys",
			Label: i18n.TCtx(ctx, "credentials.labels.kind_aws_keys"),
			Fields: []FieldSpec{
				{Name: "access_key_id", Label: i18n.TCtx(ctx, "credentials.labels.field_access_key_id"), Placeholder: "AKIA…"},
				{Name: "secret_access_key", Label: i18n.TCtx(ctx, "credentials.labels.field_secret_access_key"), Secret: true},
				{
					Name: "session_token", Label: i18n.TCtx(ctx, "credentials.labels.field_session_token"),
					Secret: true, Optional: true,
					Help: i18n.TCtx(ctx, "credentials.hints.session_token"),
				},
			},
		}},
	}
}

func (p *awsProvider) Introspect(ctx context.Context, _ string, payload map[string]string, want []string) (Introspection, error) {
	creds := awsCreds{
		AccessKeyID:     strings.TrimSpace(payload["access_key_id"]),
		SecretAccessKey: strings.TrimSpace(payload["secret_access_key"]),
		SessionToken:    strings.TrimSpace(payload["session_token"]),
	}
	if creds.AccessKeyID == "" || creds.SecretAccessKey == "" {
		return Introspection{}, apperr.Invalid("credentials.errors.aws_keys_required")
	}

	// The access key id is public by design (it travels in every signature), so it is safe as the
	// hint.
	out := Introspection{Hint: creds.AccessKeyID}

	arn, err := awsCallerIdentity(ctx, creds)
	if err != nil {
		var apiErr *awsAPIError
		if errors.As(err, &apiErr) {
			out.State = StateInvalid
			out.Err = apiErr.Message
			return out, nil
		}
		return Introspection{}, err
	}
	out.State = StateValid
	out.Identity = arn

	var notes []string
	// GetCallerIdentity does not report when temporary credentials expire, so ExpiresAt stays zero.
	if creds.SessionToken != "" {
		notes = append(notes, i18n.Text("credentials.notes.temporary_credentials"))
	}

	if actions := wantForProvider("aws", want); len(actions) > 0 {
		allowed, simErr := awsSimulate(ctx, creds, arn, actions)
		if simErr != nil {
			// Lacking iam:SimulatePrincipalPolicy is normal for least-privilege keys; leave
			// capabilities undiscovered rather than recording none.
			notes = append(notes, i18n.Text("credentials.notes.capabilities_not_discovered", "reason", apperr.Text(simErr)))
		} else {
			out.Capabilities = namespaced("aws", allowed)
		}
	}
	out.Err = strings.Join(notes, "; ")
	return out, nil
}

func awsCallerIdentity(ctx context.Context, creds awsCreds) (string, error) {
	form := url.Values{"Action": {"GetCallerIdentity"}, "Version": {"2011-06-15"}} //i18n:ignore AWS query-protocol parameter names
	var parsed struct {
		XMLName xml.Name `xml:"GetCallerIdentityResponse"`
		Result  struct {
			Arn     string `xml:"Arn"`
			Account string `xml:"Account"`
		} `xml:"GetCallerIdentityResult"`
	}
	endpoint, err := awsSTSURL()
	if err != nil {
		return "", err
	}
	if err := awsPost(ctx, endpoint, "sts", creds, form, &parsed); err != nil {
		return "", err
	}
	if parsed.Result.Arn == "" {
		return "", apperr.Unavailable("credentials.errors.sts_no_arn")
	}
	return parsed.Result.Arn, nil
}

// STS reports assumed roles in this shape, which SimulatePrincipalPolicy rejects as
// PolicySourceArn; it needs the role ARN.
var assumedRoleARN = regexp.MustCompile(`^arn:([^:]+):sts::(\d+):assumed-role/([^/]+)/.*$`)

func policySourceARN(arn string) string {
	if m := assumedRoleARN.FindStringSubmatch(arn); m != nil {
		return fmt.Sprintf("arn:%s:iam::%s:role/%s", m[1], m[2], m[3]) //i18n:ignore ARN format, an identifier
	}
	return arn
}

func awsSimulate(ctx context.Context, creds awsCreds, arn string, actions []string) ([]string, error) {
	form := url.Values{
		//i18n:ignore AWS query-protocol parameter name
		"Action": {"SimulatePrincipalPolicy"},
		//i18n:ignore AWS query-protocol parameter name
		"Version":         {"2010-05-08"},
		"PolicySourceArn": {policySourceARN(arn)},
	}
	for i, a := range actions {
		form.Set(fmt.Sprintf("ActionNames.member.%d", i+1), a)
	}
	var parsed struct {
		XMLName xml.Name `xml:"SimulatePrincipalPolicyResponse"`
		Result  struct {
			Results []struct {
				Action   string `xml:"EvalActionName"`
				Decision string `xml:"EvalDecision"`
			} `xml:"EvaluationResults>member"`
		} `xml:"SimulatePrincipalPolicyResult"`
	}
	endpoint, err := awsIAMURL()
	if err != nil {
		return nil, err
	}
	if err := awsPost(ctx, endpoint, "iam", creds, form, &parsed); err != nil {
		return nil, err
	}
	var allowed []string
	for _, r := range parsed.Result.Results {
		if r.Decision == "allowed" {
			allowed = append(allowed, r.Action)
		}
	}
	sort.Strings(allowed)
	return allowed, nil
}

// AWS-reported errors become StateInvalid; transport failures stay errors. Keep the two types
// apart.
type awsAPIError struct {
	Code    string
	Message string
	Status  int
}

func (e *awsAPIError) Error() string {
	if e.Code == "" {
		return i18n.Log("credentials.errors.aws_http_status", "status", e.Status)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func awsPost(ctx context.Context, endpoint, service string, creds awsCreds, form url.Values, out any) error {
	body := []byte(form.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/xml")
	if signErr := signV4(req, body, creds, awsSigningRegionFor(os.Getenv), service, time.Now()); signErr != nil {
		return signErr
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		return apperr.Wrap(err, "credentials.errors.read_response", "service", service)
	}
	if resp.StatusCode != http.StatusOK {
		return parseAWSError(raw, resp.StatusCode)
	}
	if err := xml.Unmarshal(raw, out); err != nil {
		return apperr.Wrap(err, "credentials.errors.decode_response", "service", service)
	}
	return nil
}

func parseAWSError(raw []byte, status int) error {
	var doc struct {
		Error struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		} `xml:"Error"`
	}
	if err := xml.Unmarshal(raw, &doc); err == nil && doc.Error.Code != "" {
		return &awsAPIError{Code: doc.Error.Code, Message: doc.Error.Message, Status: status}
	}
	return &awsAPIError{Status: status}
}
