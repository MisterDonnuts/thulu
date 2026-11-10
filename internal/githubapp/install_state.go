package githubapp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

// The state and installation_id in GitHub's redirect can be typed by anyone. A claim needs an
// unused state presented with its browser's binder,
// an installation unclaimed or already this workspace's (moves only via ReassignInstallation),
// and in SaaS mode the install OAuth code proving the user can see the installation
// (VerifyInstaller).

const InstallStateTTL = 15 * time.Minute

const maxInstallationPages = 10

const envSaaSMode = "OCTOPUS_SAAS_MODE"

// Unknown, used, expired and wrong-binder states share one error on purpose.
var ErrStateInvalid error = apperr.Invalid("githubapp.errors.state")

var ErrInstallationClaimed error = apperr.Conflict("githubapp.errors.installation_claimed")

var ErrInstallerNotAuthorized error = apperr.Forbidden("githubapp.errors.installer_not_authorized")

type InstallState struct {
	WorkspaceID string
	UserID      string
	ExpiresAt   time.Time
}

func InstallerProofRequired() bool {
	v := strings.TrimSpace(os.Getenv(envSaaSMode))
	return v == "1" || strings.EqualFold(v, "true")
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Only hashes are stored, so reading the table opens no flow.
func tokenHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func (s *Store) saveInstallState(ctx context.Context, stateHash, binderHash string, st InstallState, now time.Time) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`INSERT INTO github_install_states(state_hash, binder_hash, workspace_id, user_id, expires_at, created_at)
		 VALUES(?, ?, ?, ?, ?, ?)`),
		stateHash, binderHash, st.WorkspaceID, st.UserID, st.ExpiresAt.UTC(), now.UTC())
	return err
}

// Delete-and-return in one statement so two racing callbacks cannot both get the state.
// A wrong binder leaves the row, so someone holding only the state cannot burn the real browser's
// flow.
func (s *Store) consumeInstallState(ctx context.Context, stateHash, binderHash string, now time.Time) (*InstallState, error) {
	var st InstallState
	err := s.db.QueryRowContext(ctx, s.rb(
		`DELETE FROM github_install_states
		 WHERE state_hash = ? AND binder_hash = ? AND expires_at > ?
		 RETURNING workspace_id, user_id, expires_at`),
		stateHash, binderHash, now.UTC()).Scan(&st.WorkspaceID, &st.UserID, &st.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrStateInvalid
	}
	if err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Store) pruneInstallStates(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, s.rb(
		`DELETE FROM github_install_states WHERE expires_at <= ?`), now.UTC())
	return err
}

func (s *Service) IssueState(ctx context.Context, workspaceID, userID string) (state, binder string, err error) {
	if s.store == nil {
		return "", "", apperr.Unavailable("githubapp.errors.state_store_unavailable")
	}
	if workspaceID == "" {
		return "", "", apperr.Invalid("githubapp.errors.state_workspace_required")
	}
	if state, err = randomToken(); err != nil {
		return "", "", err
	}
	if binder, err = randomToken(); err != nil {
		return "", "", err
	}
	now := s.now()
	logx.Warn(s.store.pruneInstallStates(ctx, now), "githubapp.log.prune_states_failed")
	st := InstallState{WorkspaceID: workspaceID, UserID: userID, ExpiresAt: now.Add(InstallStateTTL)}
	if err := s.store.saveInstallState(ctx, tokenHash(state), tokenHash(binder), st, now); err != nil {
		return "", "", apperr.Wrap(err, "githubapp.errors.save_state")
	}
	return state, binder, nil
}

func (s *Service) ConsumeState(ctx context.Context, state, binder string) (*InstallState, error) {
	if s.store == nil {
		return nil, apperr.Unavailable("githubapp.errors.state_store_unavailable")
	}
	if state == "" || binder == "" {
		return nil, ErrStateInvalid
	}
	return s.store.consumeInstallState(ctx, tokenHash(state), tokenHash(binder), s.now())
}

func (s *Service) VerifyInstaller(ctx context.Context, code string, installationID int64) (string, error) {
	token, err := s.exchangeUserCode(ctx, code)
	if err != nil {
		return "", err
	}
	authz := "token " + token //i18n:ignore HTTP auth scheme
	var user struct {
		Login string `json:"login"`
	}
	if err := s.doJSON(ctx, http.MethodGet, s.apiBase+"/user", authz, nil, http.StatusOK, &user); err != nil {
		return "", err
	}
	for page := 1; page <= maxInstallationPages; page++ {
		var out struct {
			Installations []struct {
				ID int64 `json:"id"`
			} `json:"installations"`
		}
		target := fmt.Sprintf("%s/user/installations?per_page=100&page=%d", s.apiBase, page) //i18n:ignore API path and query
		if err := s.doJSON(ctx, http.MethodGet, target, authz, nil, http.StatusOK, &out); err != nil {
			return "", err
		}
		for _, inst := range out.Installations {
			if inst.ID == installationID {
				return user.Login, nil
			}
		}
		if len(out.Installations) < 100 {
			break
		}
	}
	return user.Login, ErrInstallerNotAuthorized
}
