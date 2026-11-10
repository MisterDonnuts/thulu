package mcpoauth

import (
	"context"
	"errors"
	"net/url"

	"github.com/MisterDonnuts/thulu/internal/apperr"
	"github.com/MisterDonnuts/thulu/internal/credentials"
	"github.com/MisterDonnuts/thulu/internal/logx"
)

func (s *Service) AccessToken(ctx context.Context, workspaceID, id, serverURL string) (string, error) {
	conn, err := s.load(ctx, workspaceID, id, serverURL)
	if err != nil {
		return "", err
	}
	if conn.Status == credentials.OAuthConnected && conn.AccessToken != "" && !s.expiring(conn) {
		return conn.AccessToken, nil
	}
	return s.renew(ctx, workspaceID, id, serverURL, conn.AccessToken)
}

func (s *Service) Renew(ctx context.Context, workspaceID, id, serverURL, rejected string) (string, error) {
	if _, err := s.load(ctx, workspaceID, id, serverURL); err != nil {
		return "", err
	}
	return s.renew(ctx, workspaceID, id, serverURL, rejected)
}

// One refresh per credential (singleflight). Detached from the caller's context so a hang-up
// neither fails the waiters
// nor loses a rotated refresh token half-way.
func (s *Service) renew(ctx context.Context, workspaceID, id, serverURL, stale string) (string, error) {
	ch := s.refreshes.DoChan(workspaceID+"/"+id, func() (any, error) {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), callTimeout)
		defer cancel()
		return s.refresh(rctx, workspaceID, id, serverURL, stale)
	})
	select {
	case res := <-ch:
		if res.Err != nil {
			return "", res.Err
		}
		tok, isTok := res.Val.(string)
		if !isTok || tok == "" {
			return "", ErrReconnect
		}
		return tok, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Reload first: another flight or a reconnect may already have replaced the token.
func (s *Service) refresh(ctx context.Context, workspaceID, id, serverURL, stale string) (string, error) {
	conn, err := s.load(ctx, workspaceID, id, serverURL)
	if err != nil {
		return "", err
	}
	if conn.Status == credentials.OAuthConnected && conn.AccessToken != "" && conn.AccessToken != stale && !s.expiring(conn) {
		return conn.AccessToken, nil
	}
	if conn.Status == credentials.OAuthDisconnected || conn.RefreshToken == "" {
		if conn.Status == credentials.OAuthConnected {
			s.fail(ctx, workspaceID, id, conn, "")
		}
		return "", ErrReconnect
	}
	err = s.requestToken(ctx, &conn, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {conn.RefreshToken},
	})
	var te *TokenError
	switch {
	case errors.As(err, &te) && te.Permanent:
		s.fail(ctx, workspaceID, id, conn, te.Code+" "+te.Detail)
		return "", ErrReconnect
	case errors.As(err, &te):
		return "", apperr.WrapKind(apperr.KindUnavailable, te.apperr(), "mcpoauth.errors.refresh_failed")
	case err != nil:
		return "", err
	}
	if err := s.creds.SetPayload(ctx, workspaceID, id, conn.Payload(), conn.Introspection()); err != nil {
		// The server already rotated the refresh token; losing the new one strands the connection.
		return "", apperr.Wrap(err, "mcpoauth.errors.save_tokens")
	}
	return conn.AccessToken, nil
}

func (s *Service) fail(ctx context.Context, workspaceID, id string, conn credentials.OAuthConnection, detail string) {
	conn.Status, conn.Error = credentials.OAuthFailed, detail
	conn.AccessToken, conn.RefreshToken = "", ""
	logx.Warn(s.creds.SetPayload(ctx, workspaceID, id, conn.Payload(), conn.Introspection()),
		"mcpoauth.log.mark_failed_failed", "id", id)
}

func (s *Service) Disconnect(ctx context.Context, workspaceID, id string) error {
	conn, err := s.load(ctx, workspaceID, id, "")
	if err != nil {
		return err
	}
	s.revoke(ctx, conn, conn.RefreshToken, "refresh_token")
	s.revoke(ctx, conn, conn.AccessToken, "access_token")
	conn.Status, conn.Error = credentials.OAuthDisconnected, ""
	conn.AccessToken, conn.RefreshToken = "", ""
	return s.creds.SetPayload(ctx, workspaceID, id, conn.Payload(), conn.Introspection())
}

func (s *Service) load(ctx context.Context, workspaceID, id, serverURL string) (credentials.OAuthConnection, error) {
	cred, err := s.creds.Get(ctx, workspaceID, id)
	if err != nil {
		return credentials.OAuthConnection{}, err
	}
	if !credentials.IsOAuth(cred.Provider) {
		return credentials.OAuthConnection{}, ErrNotOAuth
	}
	payload, err := s.creds.Payload(ctx, workspaceID, id)
	if err != nil {
		return credentials.OAuthConnection{}, err
	}
	conn := credentials.ParseOAuthConnection(payload)
	if serverURL != "" && !Covers(conn.Resource, serverURL) {
		return credentials.OAuthConnection{}, ErrWrongResource
	}
	return conn, nil
}

func (s *Service) expiring(conn credentials.OAuthConnection) bool {
	return !conn.ExpiresAt.IsZero() && !conn.ExpiresAt.After(s.now().Add(refreshSkew))
}
