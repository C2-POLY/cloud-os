package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// CookieName is the session cookie.
const CookieName = "cloudos_session"

type ctxKey struct{}

// AccountFrom returns the signed-in account that RequireSession put in the context.
func AccountFrom(ctx context.Context) (Account, bool) {
	a, ok := ctx.Value(ctxKey{}).(Account)
	return a, ok
}

// TokenFrom reads the session token from the cookie, or from an
// "Authorization: Bearer" header for clients without cookies (the mobile app).
func TokenFrom(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if c, err := r.Cookie(CookieName); err == nil {
		return c.Value
	}
	return ""
}

// SetCookie sends the session cookie. It is Secure whenever the request came
// over HTTPS, directly or through a proxy.
func SetCookie(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearCookie removes the session cookie.
func ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

// RequireSession answers 401 unless the request carries a valid session, and
// otherwise puts the account in the request's context.
func (s *Service) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, err := s.Authenticate(r.Context(), TokenFrom(r))
		if errors.Is(err, ErrNoSession) {
			writeError(w, http.StatusUnauthorized, ErrNoSession.Error())
			return
		}
		if err != nil {
			slog.Error("authenticate", "err", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, a)))
	})
}

// RequireAdmin answers 403 unless the signed-in account is an admin. It goes
// inside RequireSession.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a, ok := AccountFrom(r.Context()); !ok || !a.IsAdmin() {
			writeError(w, http.StatusForbidden, "admins only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ClientHeader marks a request from a Cloud OS client. A raw upload carries it
// instead of a JSON body.
const ClientHeader = "X-Cloudos-Client"

// RequireJSON refuses state-changing requests that are neither JSON nor marked
// with ClientHeader. A browser cannot send either cross-site without a CORS
// preflight, which the core never grants, so this blocks cross-site request forgery.
func RequireJSON(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// WebDAV clients send PROPFIND, PUT, MOVE… with HTTP Basic. A browser
		// cannot send those methods cross-site without a CORS preflight, which
		// the core never grants; a POST to /dav is only a read, like GET.
		if r.URL.Path == "/dav" || strings.HasPrefix(r.URL.Path, "/dav/") {
			next.ServeHTTP(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			ct := r.Header.Get("Content-Type")
			if ct != "application/json" && !strings.HasPrefix(ct, "application/json;") && r.Header.Get(ClientHeader) == "" {
				writeError(w, http.StatusUnsupportedMediaType, "send application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
