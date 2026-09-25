package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	// ErrInvalidSession means the cookie is absent, malformed, or fails
	// authentication. The caller must treat the request as unauthenticated.
	ErrInvalidSession = errors.New("invalid session")
	// ErrExpiredSession means the cookie authenticated but has expired.
	ErrExpiredSession = errors.New("expired session")
)

// SessionData is the authenticated identity carried by a session cookie.
type SessionData struct {
	Username  string    `json:"u"`
	CSRFToken string    `json:"c"`
	ExpiresAt time.Time `json:"e"`
}

// Manager issues and verifies operator sessions. The cookie is encrypted and
// authenticated with AES-256-GCM, so its contents are both confidential and
// tamper-evident without server-side storage.
type Manager struct {
	cfg    Config
	aead   cipher.AEAD
	now    func() time.Time
	secret []byte
}

// NewManager builds a session manager from a resolved Config.
func NewManager(cfg Config) (*Manager, error) {
	block, err := aes.NewCipher(cfg.SessionKey)
	if err != nil {
		return nil, fmt.Errorf("create session cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create session aead: %w", err)
	}
	return &Manager{cfg: cfg, aead: aead, now: time.Now, secret: cfg.SessionKey}, nil
}

// Username returns the configured operator username.
func (m *Manager) Username() string { return m.cfg.Username }

// DemoMode reports whether non-secret demo defaults are in use.
func (m *Manager) DemoMode() bool { return m.cfg.DemoMode }

// VerifyCredentials reports whether the username and password match the
// configured operator credential in constant time.
func (m *Manager) VerifyCredentials(username, password string) bool {
	if subtle.ConstantTimeCompare([]byte(username), []byte(m.cfg.Username)) != 1 {
		// Still run a bcrypt comparison so a wrong username and a wrong
		// password take comparable time.
		_ = bcrypt.CompareHashAndPassword(m.cfg.passwordHash, []byte(password))
		return false
	}
	return bcrypt.CompareHashAndPassword(m.cfg.passwordHash, []byte(password)) == nil
}

// Issue creates a new authenticated session for the configured operator and
// returns the cookie value plus its data.
func (m *Manager) Issue() (string, SessionData, error) {
	csrf, err := randomToken()
	if err != nil {
		return "", SessionData{}, err
	}
	data := SessionData{
		Username:  m.cfg.Username,
		CSRFToken: csrf,
		ExpiresAt: m.now().Add(m.cfg.SessionTTL).UTC(),
	}
	value, err := m.encode(data)
	if err != nil {
		return "", SessionData{}, err
	}
	return value, data, nil
}

// Parse authenticates and decrypts a cookie value. It returns ErrExpiredSession
// for an authentic but stale session and ErrInvalidSession otherwise.
func (m *Manager) Parse(value string) (SessionData, error) {
	if value == "" {
		return SessionData{}, ErrInvalidSession
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return SessionData{}, ErrInvalidSession
	}
	nonceSize := m.aead.NonceSize()
	if len(raw) < nonceSize+m.aead.Overhead() {
		return SessionData{}, ErrInvalidSession
	}
	nonce, ciphertext := raw[:nonceSize], raw[nonceSize:]
	plaintext, err := m.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return SessionData{}, ErrInvalidSession
	}
	var data SessionData
	if err := json.Unmarshal(plaintext, &data); err != nil {
		return SessionData{}, ErrInvalidSession
	}
	if data.Username == "" || data.CSRFToken == "" {
		return SessionData{}, ErrInvalidSession
	}
	if !data.ExpiresAt.After(m.now()) {
		return SessionData{}, ErrExpiredSession
	}
	return data, nil
}

// ValidCSRF reports whether the supplied token matches the session token.
func (d SessionData) ValidCSRF(token string) bool {
	if token == "" || d.CSRFToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(d.CSRFToken)) == 1
}

// Cookie builds the Set-Cookie for a session value.
func (m *Manager) Cookie(value string) *http.Cookie {
	return &http.Cookie{
		Name:     m.cfg.CookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   m.cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(m.cfg.SessionTTL.Seconds()),
	}
}

// ClearCookie builds a Set-Cookie that expires the session cookie.
func (m *Manager) ClearCookie() *http.Cookie {
	return &http.Cookie{
		Name:     m.cfg.CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   m.cfg.CookieSecure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	}
}

// CookieName returns the configured cookie name.
func (m *Manager) CookieName() string { return m.cfg.CookieName }

func (m *Manager) encode(data SessionData) (string, error) {
	plaintext, err := json.Marshal(data)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, m.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := m.aead.Seal(nil, nonce, plaintext, nil)
	return base64.RawURLEncoding.EncodeToString(append(nonce, sealed...)), nil
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
