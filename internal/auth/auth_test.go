package auth

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func envFrom(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestLoadConfigProductionRequiresSecrets(t *testing.T) {
	cases := []map[string]string{
		{},
		{"STOCKFLOW_OPERATOR_USERNAME": "operator"},
		{"STOCKFLOW_OPERATOR_USERNAME": "operator", "STOCKFLOW_OPERATOR_PASSWORD_HASH": "hash"},
		{"STOCKFLOW_OPERATOR_PASSWORD_HASH": "hash", "STOCKFLOW_SESSION_SECRET": "secret"},
	}
	for i, values := range cases {
		if _, err := LoadConfig(envFrom(values)); err == nil {
			t.Fatalf("case %d: expected missing-secret error", i)
		}
	}
}

func TestLoadConfigDemoModeProvidesDefaults(t *testing.T) {
	cfg, err := LoadConfig(envFrom(map[string]string{"STOCKFLOW_DEMO_MODE": "true"}))
	if err != nil {
		t.Fatalf("demo config: %v", err)
	}
	if !cfg.DemoMode || cfg.Username != defaultUsername {
		t.Fatalf("unexpected demo config: %+v", cfg)
	}
	if cfg.CookieSecure {
		t.Fatal("demo mode should not force Secure cookies (plain http local demo)")
	}
	manager, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("demo manager: %v", err)
	}
	if !manager.VerifyCredentials(defaultUsername, defaultDemoPassword) {
		t.Fatal("demo default credentials should verify")
	}
	if manager.VerifyCredentials(defaultUsername, "wrong") {
		t.Fatal("wrong demo password should not verify")
	}
}

func TestLoadConfigRejectsInvalidHash(t *testing.T) {
	_, err := LoadConfig(envFrom(map[string]string{
		"STOCKFLOW_OPERATOR_USERNAME":      "operator",
		"STOCKFLOW_OPERATOR_PASSWORD_HASH": "not-a-bcrypt-hash",
		"STOCKFLOW_SESSION_SECRET":         "secret",
	}))
	if err == nil {
		t.Fatal("expected invalid hash error")
	}
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	hash, err := GeneratePasswordHash("correct horse battery staple")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	manager, err := NewManager(Config{
		Username:         "operator",
		passwordHash:     []byte(hash),
		SessionKey:       []byte("0123456789abcdef0123456789abcdef"),
		SessionTTL:       time.Hour,
		CookieName:       SessionCookieName,
		LoginMaxAttempts: 5,
		LoginWindow:      time.Minute,
	})
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	return manager
}

func TestVerifyCredentials(t *testing.T) {
	manager := newTestManager(t)
	if !manager.VerifyCredentials("operator", "correct horse battery staple") {
		t.Fatal("correct credentials should verify")
	}
	if manager.VerifyCredentials("operator", "wrong") {
		t.Fatal("wrong password should not verify")
	}
	if manager.VerifyCredentials("someone-else", "correct horse battery staple") {
		t.Fatal("wrong username should not verify")
	}
}

func TestSessionRoundTripAndTamper(t *testing.T) {
	manager := newTestManager(t)
	value, data, err := manager.Issue()
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	parsed, err := manager.Parse(value)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if parsed.Username != data.Username || parsed.CSRFToken != data.CSRFToken {
		t.Fatalf("round trip mismatch: %+v vs %+v", parsed, data)
	}
	if !parsed.ValidCSRF(data.CSRFToken) {
		t.Fatal("issued CSRF token should validate")
	}
	if parsed.ValidCSRF("forged") {
		t.Fatal("forged CSRF token should not validate")
	}

	// Mutate an authenticated ciphertext byte, then re-encode it. Mutating a
	// base64 character directly can change only unused padding bits and leave
	// the decoded bytes unchanged for some token lengths.
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		t.Fatalf("decode issued session: %v", err)
	}
	raw[len(raw)-1] ^= 0x01
	tampered := base64.RawURLEncoding.EncodeToString(raw)
	if _, err := manager.Parse(tampered); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("tampered session error = %v, want ErrInvalidSession", err)
	}
	for _, bad := range []string{"", "not-base64!!", "AAAA"} {
		if _, err := manager.Parse(bad); !errors.Is(err, ErrInvalidSession) {
			t.Fatalf("Parse(%q) error = %v, want ErrInvalidSession", bad, err)
		}
	}
}

func TestSessionExpiry(t *testing.T) {
	manager := newTestManager(t)
	base := time.Now()
	manager.now = func() time.Time { return base }

	value, _, err := manager.Issue()
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := manager.Parse(value); err != nil {
		t.Fatalf("fresh session should parse: %v", err)
	}

	manager.now = func() time.Time { return base.Add(2 * time.Hour) }
	if _, err := manager.Parse(value); !errors.Is(err, ErrExpiredSession) {
		t.Fatalf("expired session error = %v, want ErrExpiredSession", err)
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	manager := newTestManager(t)
	cookie := manager.Cookie("value")
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.MaxAge <= 0 {
		t.Fatalf("session cookie is not HttpOnly/SameSite/path/bounded: %+v", cookie)
	}
	cleared := manager.ClearCookie()
	if cleared.MaxAge != -1 || cleared.Value != "" {
		t.Fatalf("clear cookie should expire the session: %+v", cleared)
	}
}

func TestLoginLimiter(t *testing.T) {
	limiter := NewLoginLimiter(3, time.Minute)
	key := "10.0.0.1"
	for i := 0; i < 3; i++ {
		if limiter.Blocked(key) {
			t.Fatalf("blocked before reaching the limit at attempt %d", i)
		}
		limiter.RecordFailure(key)
	}
	if !limiter.Blocked(key) {
		t.Fatal("expected the key to be blocked at the limit")
	}
	if limiter.Blocked("10.0.0.2") {
		t.Fatal("unrelated key should not be blocked")
	}
	limiter.Reset(key)
	if limiter.Blocked(key) {
		t.Fatal("reset should clear the failure history")
	}
}

func TestLoginLimiterWindowExpiry(t *testing.T) {
	limiter := NewLoginLimiter(1, 50*time.Millisecond)
	now := time.Now()
	limiter.now = func() time.Time { return now }
	limiter.RecordFailure("key")
	if !limiter.Blocked("key") {
		t.Fatal("expected block within the window")
	}
	now = now.Add(2 * time.Minute)
	if limiter.Blocked("key") {
		t.Fatal("expected the failure to age out of the window")
	}
}

func TestGenerateSecretIsHex(t *testing.T) {
	secret, err := NewRandomSecret()
	if err != nil {
		t.Fatalf("secret: %v", err)
	}
	if len(secret) != 64 || strings.ToLower(secret) != secret {
		t.Fatalf("unexpected secret shape: %q", secret)
	}
}
