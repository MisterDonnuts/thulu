package auth

import (
	"context"
	"fmt"
	"time"
)

func (h *Handler) ProvisionSessionForTest(email, name string) (userID, access, refresh string, err error) {
	user, err := h.store.CreateUser(context.Background(), email, name)
	if err != nil {
		return "", "", "", err
	}
	slug := slugify(name)
	if slug == "" {
		slug = "workspace"
	}
	ws, err := h.store.CreateWorkspace(context.Background(), user.ID, name+"'s workspace", slug)
	if err != nil {
		return "", "", "", err
	}
	tokens, err := h.issueTokenPair(context.Background(), user, ws.ID, RoleOwner, deviceInfo{})
	if err != nil {
		return "", "", "", err
	}
	return user.ID, tokens.AccessToken, tokens.RefreshToken, nil
}

func (s *UserStore) StoreRefreshToken(userID, workspaceID, tokenHash string, expiresAt time.Time) error {
	_, err := s.storeRefreshTokenInFamily(context.Background(), userID, workspaceID, tokenHash, "", time.Time{}, expiresAt, "", "")
	return err
}

func (s *UserStore) ValidateRefreshToken(tokenHash string) (userID, workspaceID string, err error) {
	rec, err := s.LookupRefreshToken(context.Background(), tokenHash)
	if err != nil {
		return "", "", err
	}
	switch rec.State {
	case RefreshValid:
		return rec.UserID, rec.WorkspaceID, nil
	case RefreshRevoked:
		return "", "", fmt.Errorf("token revoked")
	case RefreshExpired:
		return "", "", fmt.Errorf("token expired")
	default:
		return "", "", fmt.Errorf("token not found")
	}
}

func (s *UserStore) LookupAPIKey(rawKey string) string {
	info := s.LookupAPIKeyInfo(context.Background(), rawKey)
	if info == nil {
		return ""
	}
	return info.ID
}
