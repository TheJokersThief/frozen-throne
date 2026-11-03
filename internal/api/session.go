package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
)

// Actor is the authenticated user behind a request.
type Actor struct {
	User      model.User
	Token     string // the user's GitHub OAuth access token (decrypted)
	SessionID string
}

// sessionCodec signs cookie values and encrypts stored GitHub tokens, using
// keys derived (with distinct labels) from one secret.
type sessionCodec struct {
	signKey []byte
	aead    cipher.AEAD
}

func newSessionCodec(secret []byte) *sessionCodec {
	derive := func(label string) []byte {
		h := sha256.New()
		h.Write([]byte(label))
		h.Write([]byte{0})
		h.Write(secret)
		return h.Sum(nil)
	}
	block, _ := aes.NewCipher(derive("frozen-throne/token-encryption")) // 32-byte key: cannot fail
	aead, _ := cipher.NewGCM(block)
	return &sessionCodec{signKey: derive("frozen-throne/cookie-signing"), aead: aead}
}

func (c *sessionCodec) mac(data []byte) []byte {
	m := hmac.New(sha256.New, c.signKey)
	m.Write(data)
	return m.Sum(nil)
}

// newToken returns 32 random bytes: the secret half of a session cookie.
func newToken() ([]byte, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return b, err
}

// cookieValue is base64(token) "." base64(HMAC(token)).
func (c *sessionCodec) cookieValue(token []byte) string {
	return base64.RawURLEncoding.EncodeToString(token) + "." + base64.RawURLEncoding.EncodeToString(c.mac(token))
}

func (c *sessionCodec) parseCookie(v string) ([]byte, bool) {
	a, b, ok := strings.Cut(v, ".")
	if !ok {
		return nil, false
	}
	token, err1 := base64.RawURLEncoding.DecodeString(a)
	sig, err2 := base64.RawURLEncoding.DecodeString(b)
	if err1 != nil || err2 != nil || len(token) != 32 || !hmac.Equal(sig, c.mac(token)) {
		return nil, false
	}
	return token, true
}

// sessionID is what the store keeps: a hash of the cookie secret, so a
// database leak does not yield usable cookies.
func sessionID(token []byte) string {
	h := sha256.Sum256(token)
	return hex.EncodeToString(h[:])
}

func (c *sessionCodec) seal(plain string) []byte {
	nonce := make([]byte, c.aead.NonceSize())
	_, _ = rand.Read(nonce)
	return c.aead.Seal(nonce, nonce, []byte(plain), nil)
}

func (c *sessionCodec) open(sealed []byte) (string, error) {
	n := c.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("sealed value too short")
	}
	p, err := c.aead.Open(nil, sealed[:n], sealed[n:], nil)
	return string(p), err
}

// signed values (OAuth state) -------------------------------------------

func (c *sessionCodec) sign(payload []byte) string {
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(c.mac(append([]byte("state:"), payload...)))
}

func (c *sessionCodec) verify(v string) ([]byte, bool) {
	a, b, ok := strings.Cut(v, ".")
	if !ok {
		return nil, false
	}
	payload, err1 := base64.RawURLEncoding.DecodeString(a)
	sig, err2 := base64.RawURLEncoding.DecodeString(b)
	if err1 != nil || err2 != nil || !hmac.Equal(sig, c.mac(append([]byte("state:"), payload...))) {
		return nil, false
	}
	return payload, true
}

// authenticate resolves the session cookie into an Actor.
func (s *Server) authenticate(r *http.Request) (*Actor, error) {
	ck, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, errUnauthorized("login required")
	}
	token, ok := s.sess.parseCookie(ck.Value)
	if !ok {
		return nil, errUnauthorized("login required")
	}
	id := sessionID(token)
	now := s.now()
	sess, err := s.store.GetSession(r.Context(), id, now)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, errUnauthorized("login required")
		}
		return nil, err
	}
	user, err := s.store.GetUser(r.Context(), sess.UserID)
	if err != nil {
		if errors.Is(err, model.ErrNotFound) {
			return nil, errUnauthorized("login required")
		}
		return nil, err
	}
	gh, err := s.sess.open(sess.GitHubToken)
	if err != nil {
		return nil, errUnauthorized("login required")
	}
	if now.Sub(sess.LastSeenAt) > time.Minute {
		_ = s.store.TouchSession(r.Context(), id, now)
	}
	return &Actor{User: user, Token: gh, SessionID: id}, nil
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token []byte, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: s.sess.cookieValue(token), Path: "/",
		Expires: expires, HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
}
