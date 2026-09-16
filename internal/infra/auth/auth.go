package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/domain/apperr"
	"github.com/matheusgoncalves/jungle-wallet-go/internal/infra/config"
	"go.uber.org/fx"
)

const (
	RoleWageringWrite = "wagering:write"
	RoleWageringRead  = "wagering:read"
	RoleWalletAdmin   = "wallet:admin"
)

type ctxKey struct{}

// Principal is the authenticated caller.
type Principal struct {
	Subject    string
	ProviderID string // empty for internal-service
	Roles      map[string]bool
}

func (p Principal) HasRole(role string) bool { return p.Roles[role] }
func (p Principal) IsInternal() bool         { return p.HasRole(RoleWalletAdmin) && p.ProviderID == "" }

// FromContext returns the authenticated principal.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

var Module = fx.Module("auth",
	fx.Provide(NewVerifier),
)

// Verifier validates bearer JWTs against the IdP JWKS.
type Verifier struct {
	verifier *oidc.IDTokenVerifier
}

func NewVerifier(cfg config.Config) (*Verifier, error) {
	ctx := context.Background()
	discovery := cfg.OIDCDiscoveryURL
	if discovery == "" {
		discovery = cfg.OIDCIssuerURL
	}
	if discovery != cfg.OIDCIssuerURL {
		// Token iss (host-published Keycloak) differs from in-cluster discovery URL.
		ctx = oidc.InsecureIssuerURLContext(ctx, cfg.OIDCIssuerURL)
	}
	provider, err := oidc.NewProvider(ctx, discovery)
	if err != nil {
		return nil, fmt.Errorf("oidc provider: %w", err)
	}
	v := provider.Verifier(&oidc.Config{
		ClientID: cfg.OIDCAudience,
		// Resource server accepts tokens from multiple clients; authorize via roles + provider_id.
		SkipClientIDCheck:    cfg.OIDCAudience == "" || cfg.OIDCAudience == "*",
		SupportedSigningAlgs: []string{"RS256"},
	})
	return &Verifier{verifier: v}, nil
}

type claims struct {
	Sub         string `json:"sub"`
	ProviderID  string `json:"provider_id"`
	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

func (v *Verifier) Authenticate(ctx context.Context, rawToken string) (Principal, error) {
	token, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return Principal{}, apperr.WrapFailure(apperr.CodeInvalidInput, "invalid token", apperr.ErrUnauthorized)
	}
	var c claims
	if err := token.Claims(&c); err != nil {
		return Principal{}, apperr.WrapFailure(apperr.CodeInvalidInput, "invalid claims", apperr.ErrUnauthorized)
	}
	roles := map[string]bool{}
	for _, r := range c.RealmAccess.Roles {
		roles[r] = true
	}
	return Principal{Subject: c.Sub, ProviderID: c.ProviderID, Roles: roles}, nil
}

// Middleware requires a valid bearer token and injects Principal.
func (v *Verifier) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		if h == "" || !strings.HasPrefix(strings.ToLower(h), "bearer ") {
			writeUnauthorized(w, "missing bearer token")
			return
		}
		raw := strings.TrimSpace(h[7:])
		p, err := v.Authenticate(r.Context(), raw)
		if err != nil {
			writeUnauthorized(w, "invalid or expired token")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKey{}, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRoles ensures at least one of the roles is present.
func RequireRoles(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := FromContext(r.Context())
			if !ok {
				writeUnauthorized(w, "unauthenticated")
				return
			}
			for _, role := range roles {
				if p.HasRole(role) {
					next.ServeHTTP(w, r)
					return
				}
			}
			writeForbidden(w, "insufficient role")
		})
	}
}

// RequireInternal restricts to wallet:admin without provider_id (internal-service).
func RequireInternal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := FromContext(r.Context())
		if !ok {
			writeUnauthorized(w, "unauthenticated")
			return
		}
		if !p.HasRole(RoleWalletAdmin) || p.ProviderID != "" {
			writeForbidden(w, "internal operation requires wallet:admin")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireProviderMatch ensures body/path providerId equals token claim when present.
func RequireProviderMatch(providerID string, p Principal) error {
	if p.HasRole(RoleWalletAdmin) && p.ProviderID == "" {
		return nil // internal may read across providers when explicitly allowed by route
	}
	if p.ProviderID == "" {
		return apperr.WrapFailure(apperr.CodeInvalidInput, "provider_id claim required", apperr.ErrForbidden)
	}
	if p.ProviderID != providerID {
		return apperr.WrapFailure(apperr.CodeInvalidInput, "providerId does not match token", apperr.ErrForbidden)
	}
	return nil
}

func writeUnauthorized(w http.ResponseWriter, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"type":"about:blank","title":"Unauthorized","status":401,"detail":%q}`, detail)))
}

func writeForbidden(w http.ResponseWriter, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(fmt.Sprintf(`{"type":"about:blank","title":"Forbidden","status":403,"detail":%q}`, detail)))
}
