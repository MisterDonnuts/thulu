package auth

import (
	"fmt"
	"testing"

	"github.com/MisterDonnuts/thulu/internal/edition"
	"github.com/MisterDonnuts/thulu/internal/edition/editiontest"
)

type seatsLicense int

func (seatsLicense) Has(edition.Feature) bool { return true }
func (l seatsLicense) Status() edition.Status {
	return edition.Status{Edition: edition.Enterprise, Features: []string{"*"}, Seats: int(l)}
}

func TestLicenseSeatsCountsSignedInMembers(t *testing.T) {
	editiontest.Community(t)
	t.Setenv("OCTOPUS_SAAS_MODE", "")
	store := newSSOTestStore(t)
	h := NewHandler(store)
	ctx := t.Context()

	owner, err := store.CreateUser(ctx, "owner@example.com", "Owner")
	if err != nil {
		t.Fatal(err)
	}
	ws, err := store.CreateWorkspace(ctx, owner.ID, "Acme", "acme")
	if err != nil {
		t.Fatal(err)
	}
	// The owner never signed in, like the placeholder `thulu init` leaves: it does not count.
	for i := range 6 {
		u, err := store.CreateUser(ctx, fmt.Sprintf("p%d@example.com", i), "P")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.LinkOIDCIdentity(ctx, u.ID, "github", fmt.Sprintf("sub-%d", i), u.Email); err != nil {
			t.Fatal(err)
		}
		if err := store.AddMember(ctx, ws.ID, u.ID, RoleDeveloper); err != nil {
			t.Fatal(err)
		}
	}

	got, err := h.licenseSeats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := licenseSeats{Applies: true, People: 6, Included: edition.CommunitySeats}
	if got != want {
		t.Errorf("Community = %+v, want %+v", got, want)
	}

	edition.SetLicense(seatsLicense(10))
	if got, _ := h.licenseSeats(ctx); got != (licenseSeats{Applies: true, People: 6, Included: 10, Licensed: true}) {
		t.Errorf("license for 10 = %+v", got)
	}
	edition.SetLicense(seatsLicense(0))
	if got, _ := h.licenseSeats(ctx); got.Applies {
		t.Errorf("a license that names no seat count must not show anything, got %+v", got)
	}
	t.Setenv("OCTOPUS_SAAS_MODE", "1")
	if got, _ := h.licenseSeats(ctx); got.Applies {
		t.Errorf("SaaS mode bills by plan, got %+v", got)
	}
}
