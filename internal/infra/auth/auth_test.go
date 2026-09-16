package auth_test

import (
	"errors"
	"testing"

	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/auth"
)

func TestRequireProviderMatch(t *testing.T) {
	provider := auth.Principal{ProviderID: "provider-a", Roles: map[string]bool{auth.RoleWageringWrite: true}}
	if err := auth.RequireProviderMatch("provider-a", provider); err != nil {
		t.Fatal(err)
	}
	err := auth.RequireProviderMatch("provider-b", provider)
	if err == nil {
		t.Fatal("expected forbidden")
	}
	if !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("want ErrForbidden, got %v", err)
	}

	internal := auth.Principal{Roles: map[string]bool{auth.RoleWalletAdmin: true}}
	if err := auth.RequireProviderMatch("provider-a", internal); err != nil {
		t.Fatal(err)
	}
}
