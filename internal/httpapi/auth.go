package httpapi

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/oliaditya05/stockflow/internal/auth"
	"github.com/oliaditya05/stockflow/internal/platform/apperr"
	"github.com/oliaditya05/stockflow/internal/platform/httpx"
)

type identityContextKey struct{}

func withIdentity(ctx context.Context, data auth.SessionData) context.Context {
	return context.WithValue(ctx, identityContextKey{}, data)
}

func identityFromContext(ctx context.Context) (auth.SessionData, bool) {
	data, ok := ctx.Value(identityContextKey{}).(auth.SessionData)
	return data, ok
}

// actorFrom returns the authenticated operator name. Every mutation derives its
// audit actor from this value, never from the request body or headers.
func actorFrom(ctx context.Context) string {
	data, ok := identityFromContext(ctx)
	if !ok {
		return ""
	}
	return data.Username
}

func (s *Server) limiter() *auth.LoginLimiter {
	if s.LoginLimiter != nil {
		return s.LoginLimiter
	}
	return auth.NewLoginLimiter(s.AuthLoginMaxAttempts, s.AuthLoginWindow)
}

// requireSession rejects requests without a valid, unexpired session cookie.
func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Auth == nil {
			httpx.WriteProblem(w, r, apperr.Internal("authentication is not configured"))
			return
		}
		cookie, err := r.Cookie(s.Auth.CookieName())
		if err != nil {
			unauthorized(w, r)
			return
		}
		data, err := s.Auth.Parse(cookie.Value)
		if err != nil {
			unauthorized(w, r)
			return
		}
		next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), data)))
	})
}

// requireCSRF enforces a matching CSRF token on state-changing methods. It must
// run after requireSession so the identity is present.
func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		data, ok := identityFromContext(r.Context())
		if !ok {
			unauthorized(w, r)
			return
		}
		if !data.ValidCSRF(strings.TrimSpace(r.Header.Get("X-CSRF-Token"))) {
			httpx.WriteProblem(w, r, apperr.New(http.StatusForbidden, "csrf_failed",
				"CSRF validation failed", "the X-CSRF-Token header is missing or does not match the session"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func unauthorized(w http.ResponseWriter, r *http.Request) {
	httpx.WriteProblem(w, r, apperr.New(http.StatusUnauthorized, "unauthenticated",
		"Authentication required", "a valid operator session is required for this endpoint"))
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type sessionResponse struct {
	Authenticated bool      `json:"authenticated"`
	Username      string    `json:"username,omitempty"`
	CSRFToken     string    `json:"csrf_token,omitempty"`
	ExpiresAt     time.Time `json:"expires_at,omitzero"`
	DemoMode      bool      `json:"demo_mode,omitempty"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var request loginRequest
	if !httpx.DecodeJSON(w, r, &request) {
		return
	}
	if s.Auth == nil {
		httpx.WriteProblem(w, r, apperr.Internal("authentication is not configured"))
		return
	}

	key := clientIP(r)
	limiter := s.limiter()
	if limiter.Blocked(key) {
		w.Header().Set("Retry-After", "60")
		httpx.WriteProblem(w, r, apperr.New(http.StatusTooManyRequests, "login_throttled",
			"Too many login attempts", "too many failed login attempts from this address; try again later"))
		return
	}

	if !s.Auth.VerifyCredentials(strings.TrimSpace(request.Username), request.Password) {
		limiter.RecordFailure(key)
		httpx.WriteProblem(w, r, apperr.New(http.StatusUnauthorized, "invalid_credentials",
			"Invalid credentials", "the supplied username or password is incorrect"))
		return
	}

	limiter.Reset(key)
	value, data, err := s.Auth.Issue()
	if err != nil {
		httpx.WriteError(w, r, s.Logger, err)
		return
	}
	http.SetCookie(w, s.Auth.Cookie(value))
	httpx.WriteJSON(w, http.StatusOK, sessionResponse{
		Authenticated: true,
		Username:      data.Username,
		CSRFToken:     data.CSRFToken,
		ExpiresAt:     data.ExpiresAt,
		DemoMode:      s.Auth.DemoMode(),
	})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if s.Auth == nil {
		httpx.WriteJSON(w, http.StatusOK, sessionResponse{Authenticated: false})
		return
	}
	cookie, err := r.Cookie(s.Auth.CookieName())
	if err != nil {
		httpx.WriteJSON(w, http.StatusOK, sessionResponse{Authenticated: false, DemoMode: s.Auth.DemoMode()})
		return
	}
	data, err := s.Auth.Parse(cookie.Value)
	if err != nil {
		httpx.WriteJSON(w, http.StatusOK, sessionResponse{Authenticated: false, DemoMode: s.Auth.DemoMode()})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sessionResponse{
		Authenticated: true,
		Username:      data.Username,
		CSRFToken:     data.CSRFToken,
		ExpiresAt:     data.ExpiresAt,
		DemoMode:      s.Auth.DemoMode(),
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.Auth != nil {
		http.SetCookie(w, s.Auth.ClearCookie())
	}
	httpx.WriteJSON(w, http.StatusOK, sessionResponse{Authenticated: false})
}

// clientIP extracts the connection peer address. It deliberately does not trust
// X-Forwarded-For: the demo runs as a single process without a trusted proxy.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
