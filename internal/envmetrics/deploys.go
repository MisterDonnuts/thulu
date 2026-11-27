package envmetrics

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

// CloudDeploy is one deploy marker thulu-cloud read for an environment (a Cloud Run revision).
type CloudDeploy struct {
	At       string `json:"at"`
	Service  string `json:"service,omitempty"`
	Revision string `json:"revision,omitempty"`
	Image    string `json:"image,omitempty"`
}

// DeployRead is what thulu-cloud answered: the markers of the window, oldest first, or why it has
// none to give (Unavailable with Reason).
type DeployRead struct {
	Deploys     []CloudDeploy `json:"deploys"`
	Unavailable bool          `json:"unavailable,omitempty"`
	Reason      string        `json:"reason,omitempty"`
}

type deploysRequest struct {
	EnvironmentID string             `json:"environment_id"`
	Environment   CollectEnvironment `json:"environment"`
	Credential    CollectCredential  `json:"credential"`
	WindowHours   int                `json:"window_hours"`
}

// RecentDeploys asks thulu-cloud for the deploy markers of envID over the last window, with the
// credential the environment is granted for the deploys tool. A deploy watch on a cloud source
// polls it; no agent and no run key take part.
func (c *Collector) RecentDeploys(ctx context.Context, envID string, window time.Duration) (*DeployRead, error) {
	if c == nil || c.cloudURL == "" {
		return nil, apperr.Unavailable("envmetrics.errors.collector_no_url")
	}
	if c.targets == nil {
		return nil, apperr.Unavailable("envmetrics.errors.collector_no_targets")
	}
	target, err := c.targets.ResolveDeployTarget(ctx, envID)
	if err != nil {
		return nil, err
	}
	hours := int(window / time.Hour)
	if hours < 1 {
		hours = 1
	}
	body, err := json.Marshal(deploysRequest{
		EnvironmentID: envID, Environment: target.Environment, Credential: target.Credential, WindowHours: hours,
	})
	if err != nil {
		return nil, apperr.Wrap(err, "envmetrics.errors.marshal_collect_request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cloudURL+"/deploys", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, apperr.WrapKind(apperr.KindUnavailable, err, "envmetrics.errors.post_deploys")
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		DeployRead
		Error string `json:"error,omitempty"`
	}
	if decErr := json.NewDecoder(resp.Body).Decode(&out); decErr != nil {
		return nil, apperr.Wrap(decErr, "envmetrics.errors.decode_deploys_response", "status", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, apperr.New("envmetrics.errors.deploys_http_status", "status", resp.StatusCode, "message", out.Error)
	}
	return &out.DeployRead, nil
}
