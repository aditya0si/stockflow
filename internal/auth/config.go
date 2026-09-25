// Package auth implements the smallest honest single-operator security model
// for StockFlow: one operator credential configured by environment, a bounded
// encrypted session cookie, CSRF protection for cookie-authenticated
// mutations, and an in-memory login throttle.
//
// It is deliberately not enterprise authentication. There are no users, roles,
// tenants, refresh tokens, or external identity providers. See the threat model
// in the README for the assumptions and residual risks.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	// SessionCookieName is the name of the encrypted session cookie.
	SessionCookieName = "stockflow_session"

	defaultUsername      = "operator"
	defaultDemoPassword  = "stockflow-demo"
	defaultSessionTTL    = 12 * time.Hour
	defaultLoginAttempts = 10
	defaultLoginWindow   = time.Minute
)

// Config is the resolved operator authentication configuration.
type Config struct {
	Username         string
	passwordHash     []byte
	SessionKey       []byte
	SessionTTL       time.Duration
	CookieSecure     bool
	CookieName       string
	DemoMode         bool
	LoginMaxAttempts int
	LoginWindow      time.Duration
}

// LoadConfig resolves the operator credential and session settings from the
// provided environment lookup.
//
// In production (DemoMode false) STOCKFLOW_OPERATOR_USERNAME,
// STOCKFLOW_OPERATOR_PASSWORD_HASH (a bcrypt hash) and STOCKFLOW_SESSION_SECRET
// are all required; a missing or malformed value is a fatal configuration
// error. In explicit demo mode a missing value falls back to a generated
// non-secret default so a local demo can start with one command.
func LoadConfig(getenv func(string) string) (Config, error) {
	demo := strings.EqualFold(strings.TrimSpace(getenv("STOCKFLOW_DEMO_MODE")), "true")

	cfg := Config{
		Username:         strings.TrimSpace(getenv("STOCKFLOW_OPERATOR_USERNAME")),
		SessionTTL:       defaultSessionTTL,
		CookieName:       SessionCookieName,
		DemoMode:         demo,
		LoginMaxAttempts: defaultLoginAttempts,
		LoginWindow:      defaultLoginWindow,
	}

	if raw := strings.TrimSpace(getenv("STOCKFLOW_SESSION_TTL")); raw != "" {
		ttl, err := time.ParseDuration(raw)
		if err != nil || ttl <= 0 {
			return Config{}, fmt.Errorf("STOCKFLOW_SESSION_TTL must be a positive duration, got %q", raw)
		}
		cfg.SessionTTL = ttl
	}

	cookieSecureDefault := !demo
	cfg.CookieSecure = parseBoolDefault(getenv("STOCKFLOW_COOKIE_SECURE"), cookieSecureDefault)

	username := cfg.Username
	hash := []byte(strings.TrimSpace(getenv("STOCKFLOW_OPERATOR_PASSWORD_HASH")))
	secret := strings.TrimSpace(getenv("STOCKFLOW_SESSION_SECRET"))

	if !demo {
		var missing []string
		if username == "" {
			missing = append(missing, "STOCKFLOW_OPERATOR_USERNAME")
		}
		if len(hash) == 0 {
			missing = append(missing, "STOCKFLOW_OPERATOR_PASSWORD_HASH")
		}
		if secret == "" {
			missing = append(missing, "STOCKFLOW_SESSION_SECRET")
		}
		if len(missing) > 0 {
			return Config{}, fmt.Errorf("authentication is not configured: set %s (or set STOCKFLOW_DEMO_MODE=true for the documented local demo)", strings.Join(missing, ", "))
		}
	}

	if username == "" {
		username = defaultUsername
	}

	if len(hash) == 0 {
		plaintext := getenv("STOCKFLOW_OPERATOR_PASSWORD")
		if plaintext == "" {
			plaintext = defaultDemoPassword
		}
		generated, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
		if err != nil {
			return Config{}, fmt.Errorf("hash demo operator password: %w", err)
		}
		hash = generated
	}

	if _, err := bcrypt.Cost(hash); err != nil {
		return Config{}, fmt.Errorf("STOCKFLOW_OPERATOR_PASSWORD_HASH is not a valid bcrypt hash: %w", err)
	}

	var key []byte
	if secret != "" {
		sum := sha256.Sum256([]byte(secret))
		key = sum[:]
	} else {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return Config{}, fmt.Errorf("generate demo session key: %w", err)
		}
	}

	cfg.Username = username
	cfg.passwordHash = hash
	cfg.SessionKey = key
	return cfg, nil
}

// GeneratePasswordHash returns a bcrypt hash that can be placed in
// STOCKFLOW_OPERATOR_PASSWORD_HASH. It is used by the operator setup helper and
// tests; a hash, never the plaintext, is what belongs in the environment.
func GeneratePasswordHash(password string) (string, error) {
	generated, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(generated), nil
}

// NewRandomSecret returns a hex-encoded random secret suitable for
// STOCKFLOW_SESSION_SECRET.
func NewRandomSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func parseBoolDefault(raw string, fallback bool) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes":
		return true
	case "false", "0", "no":
		return false
	default:
		return fallback
	}
}
