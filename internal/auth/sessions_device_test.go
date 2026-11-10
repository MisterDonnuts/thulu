package auth_test

import (
	"testing"
	"time"

	"github.com/MisterDonnuts/thulu/internal/auth"
)

func TestSessions_DeviceSupersede(t *testing.T) {
	store, err := auth.NewUserStore(tempDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	user, err := store.CreateUser(t.Context(), "alice@example.com", "Alice")
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	ws, err := store.CreateWorkspace(t.Context(), user.ID, "Alice ws", "alicews")
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	const chromeUA = "Mozilla/5.0 (Macintosh) Chrome/120"
	mint := func(ua, ip string) {
		_, hash, _ := auth.IssueRefreshToken()
		if _, err := store.StoreRefreshTokenFamily(t.Context(), user.ID, ws.ID, hash, "", time.Now(), time.Now().Add(time.Hour), ua, ip); err != nil {
			t.Fatalf("StoreRefreshTokenFamily: %v", err)
		}
	}

	mint(chromeUA, "1.2.3.4")
	mint(chromeUA, "1.2.3.4")
	mint("curl/8", "9.9.9.9")

	fams, err := store.ListSessionFamilies(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListSessionFamilies: %v", err)
	}
	if len(fams) != 3 {
		t.Fatalf("want 3 live families before supersede, got %d", len(fams))
	}

	if err := store.RevokeLiveFamiliesForDevice(t.Context(), user.ID, chromeUA, "1.2.3.4"); err != nil {
		t.Fatalf("RevokeLiveFamiliesForDevice: %v", err)
	}
	mint(chromeUA, "1.2.3.4")

	fams, err = store.ListSessionFamilies(t.Context(), user.ID)
	if err != nil {
		t.Fatalf("ListSessionFamilies: %v", err)
	}
	if len(fams) != 2 {
		t.Fatalf("want 2 live families after supersede, got %d", len(fams))
	}

	var sawChrome, sawCurl bool
	for _, f := range fams {
		switch f.DeviceUA {
		case chromeUA:
			sawChrome = true
			if f.DeviceIP != "1.2.3.4" {
				t.Errorf("chrome family IP = %q, want 1.2.3.4", f.DeviceIP)
			}
		case "curl/8":
			sawCurl = true
		}
	}
	if !sawChrome || !sawCurl {
		t.Errorf("expected both devices surfaced: chrome=%v curl=%v", sawChrome, sawCurl)
	}
}
