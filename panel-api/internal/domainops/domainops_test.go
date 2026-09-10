package domainops

import (
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func strptr(s string) *string { return &s }

// TestCheckOwnerEligible_Admin: a panel-only admin has no /home/<name>, so a
// domain cannot host under them — both adapters must refuse before persistence.
func TestCheckOwnerEligible_Admin(t *testing.T) {
	err := CheckOwnerEligible(&models.User{IsAdmin: true, Username: strptr("root")})
	if !errors.Is(err, ErrAdminCannotHost) {
		t.Fatalf("admin owner: want ErrAdminCannotHost, got %v", err)
	}
}

// TestCheckOwnerEligible_Suspended is the load-bearing gap this slice closes:
// the CLI never ran this check, so a suspended owner could get a live vhost from
// the command line. The gate must refuse a suspended owner on both adapters.
func TestCheckOwnerEligible_Suspended(t *testing.T) {
	err := CheckOwnerEligible(&models.User{Suspended: true, Username: strptr("alice")})
	if !errors.Is(err, ErrOwnerSuspended) {
		t.Fatalf("suspended owner: want ErrOwnerSuspended, got %v", err)
	}
}

// TestCheckOwnerEligible_NoUsername: a hosting user always has a username; its
// absence is an inconsistent state the adapter surfaces as an internal error.
func TestCheckOwnerEligible_NoUsername(t *testing.T) {
	if err := CheckOwnerEligible(&models.User{}); !errors.Is(err, ErrOwnerNoUsername) {
		t.Fatalf("nil username: want ErrOwnerNoUsername, got %v", err)
	}
	if err := CheckOwnerEligible(&models.User{Username: strptr("")}); !errors.Is(err, ErrOwnerNoUsername) {
		t.Fatalf("empty username: want ErrOwnerNoUsername, got %v", err)
	}
}

// TestCheckOwnerEligible_Nil guards the wiring bug — a missing owner is not a
// policy result, but it must never read as eligible.
func TestCheckOwnerEligible_Nil(t *testing.T) {
	if err := CheckOwnerEligible(nil); !errors.Is(err, ErrOwnerNil) {
		t.Fatalf("nil owner: want ErrOwnerNil, got %v", err)
	}
}

// TestCheckOwnerEligible_Eligible: a regular, unsuspended, named owner passes.
func TestCheckOwnerEligible_Eligible(t *testing.T) {
	if err := CheckOwnerEligible(&models.User{Username: strptr("alice")}); err != nil {
		t.Fatalf("eligible owner: want nil, got %v", err)
	}
}

// TestCheckOwnerEligible_Order pins the REST handler's original precedence:
// admin is reported before suspended when an owner is both. Reordering the gate
// would change which sentinel (and which adapter status code) a caller sees.
func TestCheckOwnerEligible_Order(t *testing.T) {
	err := CheckOwnerEligible(&models.User{IsAdmin: true, Suspended: true, Username: strptr("root")})
	if !errors.Is(err, ErrAdminCannotHost) {
		t.Fatalf("admin+suspended: want ErrAdminCannotHost first, got %v", err)
	}
}
