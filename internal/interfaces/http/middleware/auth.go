package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jws"
	"github.com/lestrrat-go/jwx/v2/jwt"

	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/logger"
)

// DenyListChecker is the interface for token revocation checks.
// Defined here at the consumer boundary, not in the infrastructure layer.
type DenyListChecker interface {
	IsRevoked(ctx context.Context, jti string) (bool, error)
}

// AuthConfig holds configuration for the JWT authentication middleware.
type AuthConfig struct {
	// Issuer is the expected "iss" claim. It may be a comma-separated list
	// when the service accepts both local mock-issuer tokens and gateway-issued
	// tokens during WSO2 development.
	Issuer string

	// Audience is the expected "aud" claim value.
	Audience string

	// KeySet is the primary JWK set used for signature verification.
	KeySet jwk.Set

	// AdditionalKeySet is an optional fallback JWK set. It is useful when WSO2
	// re-signs tokens with its own key while mock-issuer tokens are still valid.
	AdditionalKeySet jwk.Set

	// AllowedIssuers is an optional explicit list of accepted issuers. When it
	// is empty, Issuer is used; when Issuer contains commas, those values are
	// accepted too.
	AllowedIssuers []string

	// DenyList is the token revocation checker. It may be nil in local tests.
	DenyList DenyListChecker

	// Log is the service logger.
	Log *slog.Logger
}

// Authenticate returns a chi-compatible JWT authentication middleware.
//
// WSO2 may either pass the original Bearer token through to the backend or
// replace it with a gateway-issued JWT. The backend still validates the token
// cryptographically. This keeps direct internal calls protected even when they
// bypass the API gateway.
func Authenticate(cfg AuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := domainauth.FromContext(r.Context()); ok {
				next.ServeHTTP(w, r)
				return
			}

			log := logger.FromContext(r.Context())

			token, err := extractBearerToken(r)
			if err != nil {
				writeAuthError(w, r, domainauth.ErrMissingToken, log)
				return
			}

			parsed, err := parseJWT(token, cfg)
			if err != nil {
				log.Debug("jwt parse/validation failed", slog.String("error", err.Error()))
				if isExpiredError(err) {
					writeAuthError(w, r, domainauth.ErrTokenExpired, log)
				} else {
					writeAuthError(w, r, domainauth.ErrInvalidToken, log)
				}
				return
			}

			if !issuerAllowed(parsed.Issuer(), cfg) {
				log.Debug("jwt issuer validation failed", slog.String("iss", parsed.Issuer()))
				writeAuthError(w, r, domainauth.ErrInvalidIssuer, log)
				return
			}

			workspaceID, ok := parsed.PrivateClaims()["workspace_id"].(string)
			if !ok || workspaceID == "" {
				log.Warn("token missing workspace_id claim",
					slog.String("sub", parsed.Subject()),
					slog.String("iss", parsed.Issuer()),
				)
				writeAuthError(w, r, domainauth.ErrMissingClaim, log)
				return
			}

			jti := parsed.JwtID()
			if jti == "" {
				log.Warn("token missing jti claim", slog.String("sub", parsed.Subject()))
				writeAuthError(w, r, domainauth.ErrMissingClaim, log)
				return
			}

			if cfg.DenyList != nil {
				revoked, err := cfg.DenyList.IsRevoked(r.Context(), jti)
				if err != nil {
					log.Warn("deny list check failed, failing open",
						slog.String("error", err.Error()),
						slog.String("jti_prefix", jti[:min(8, len(jti))]),
					)
				} else if revoked {
					writeAuthError(w, r, domainauth.ErrTokenRevoked, log)
					return
				}
			}

			scope, _ := parsed.PrivateClaims()["scope"].(string)
			claims := &domainauth.Claims{
				UserID:      parsed.Subject(),
				TokenID:     jti,
				WorkspaceID: workspaceID,
				Scope:       scope,
				Issuer:      parsed.Issuer(),
				Audiences:   parsed.Audience(),
				IssuedAt:    parsed.IssuedAt(),
				ExpiresAt:   parsed.Expiration(),
			}

			ctx := domainauth.WithContext(r.Context(), claims)
			ctx = logger.WithContext(ctx, logger.WithUserContext(
				logger.FromContext(ctx),
				claims.UserID,
				claims.WorkspaceID,
			))

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func parseJWT(token string, cfg AuthConfig) (jwt.Token, error) {
	parsed, err := parseJWTWithKeySet(token, cfg.KeySet, cfg.Audience)
	if err == nil {
		return parsed, nil
	}
	if cfg.AdditionalKeySet != nil {
		if fallback, fallbackErr := parseJWTWithKeySet(token, cfg.AdditionalKeySet, cfg.Audience); fallbackErr == nil {
			return fallback, nil
		}
	}
	return nil, err
}

func parseJWTWithKeySet(token string, keySet jwk.Set, audience string) (jwt.Token, error) {
	return jwt.Parse(
		[]byte(token),
		jwt.WithKeySet(
			keySet,
			jws.WithRequireKid(false),
			jws.WithInferAlgorithmFromKey(true),
			jws.WithUseDefault(true),
		),
		jwt.WithValidate(true),
		jwt.WithAudience(audience),
		jwt.WithAcceptableSkew(30*time.Second),
	)
}

func issuerAllowed(tokenIssuer string, cfg AuthConfig) bool {
	for _, issuer := range configuredIssuers(cfg) {
		if tokenIssuer == issuer {
			return true
		}
	}
	return false
}

func configuredIssuers(cfg AuthConfig) []string {
	var issuers []string
	appendIssuer := func(value string) {
		value = strings.TrimSpace(value)
		if value != "" {
			issuers = append(issuers, value)
		}
	}
	for _, issuer := range strings.Split(cfg.Issuer, ",") {
		appendIssuer(issuer)
	}
	for _, issuer := range cfg.AllowedIssuers {
		appendIssuer(issuer)
	}
	return issuers
}

// RequireScope returns middleware that enforces a specific scope on top of
// authentication. Apply it after Authenticate in the middleware chain.
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := domainauth.FromContext(r.Context())
			if !ok || !claims.HasScope(scope) {
				response.WriteProblem(w, response.Problem{
					Type:     response.ProblemTypeUnauthorized,
					Title:    "Forbidden",
					Status:   http.StatusForbidden,
					Detail:   "Your token does not have the required scope: " + scope,
					Instance: r.URL.Path,
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func extractBearerToken(r *http.Request) (string, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", domainauth.ErrMissingToken
	}
	scheme, token, found := strings.Cut(authHeader, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", domainauth.ErrMissingToken
	}
	return token, nil
}

func writeAuthError(w http.ResponseWriter, r *http.Request, err error, log *slog.Logger) {
	log.Debug("authentication failed", slog.String("reason", err.Error()))
	response.WriteProblem(w, response.Problem{
		Type:     response.ProblemTypeUnauthenticated,
		Title:    "Unauthorized",
		Status:   http.StatusUnauthorized,
		Detail:   "Your request could not be authenticated. Provide a valid Bearer token.",
		Instance: r.URL.Path,
	})
}

func isExpiredError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "exp not satisfied")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
