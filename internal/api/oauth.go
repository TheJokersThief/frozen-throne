package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
)

// OAuthProvider abstracts the GitHub OAuth web flow so tests can fake it.
type OAuthProvider interface {
	// AuthorizeURL returns the URL to send the browser to.
	AuthorizeURL(state string) string
	// Exchange trades an authorization code for a user access token.
	Exchange(ctx context.Context, code string) (token string, err error)
	// User fetches the profile belonging to token.
	User(ctx context.Context, token string) (model.User, error)
}

// ErrBadCode is returned by Exchange when GitHub rejects the code.
var ErrBadCode = errors.New("oauth: code rejected")

type oauthState struct {
	Nonce    string `json:"n"`
	Redirect string `json:"r"`
	Expires  int64  `json:"e"`
}

const stateTTL = 10 * time.Minute

// safeRedirect accepts only same-origin absolute paths.
func safeRedirect(p string) string {
	if p == "" || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n") {
		return "/"
	}
	return p
}

func (s *Server) login(c *call) error {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	st := oauthState{
		Nonce:    base64.RawURLEncoding.EncodeToString(nonce),
		Redirect: safeRedirect(c.r.URL.Query().Get("redirect")),
		Expires:  s.now().Add(stateTTL).Unix(),
	}
	payload, _ := json.Marshal(st)
	http.SetCookie(c.w, &http.Cookie{
		Name: stateCookie, Value: st.Nonce, Path: Prefix + "/auth", MaxAge: int(stateTTL.Seconds()),
		HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(c.w, c.r, s.oauth.AuthorizeURL(s.sess.sign(payload)), http.StatusFound)
	return nil
}

func (s *Server) loginCallback(c *call) error {
	q := c.r.URL.Query()
	code, state := q.Get("code"), q.Get("state")
	if code == "" || state == "" {
		return errBadRequest("code and state are required", nil)
	}
	payload, ok := s.sess.verify(state)
	var st oauthState
	if !ok || json.Unmarshal(payload, &st) != nil || s.now().Unix() > st.Expires {
		return errBadRequest("invalid or expired state", nil)
	}
	ck, err := c.r.Cookie(stateCookie)
	if err != nil || !hmac.Equal([]byte(ck.Value), []byte(st.Nonce)) {
		return errBadRequest("state does not match this browser", nil)
	}
	http.SetCookie(c.w, &http.Cookie{Name: stateCookie, Value: "", Path: Prefix + "/auth", MaxAge: -1,
		HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode})

	token, err := s.oauth.Exchange(c.r.Context(), code)
	if err != nil {
		if errors.Is(err, ErrBadCode) {
			return errBadRequest("GitHub rejected the login code", nil)
		}
		return err
	}
	user, err := s.oauth.User(c.r.Context(), token)
	if err != nil {
		return err
	}
	now := s.now()
	user.UpdatedAt = now
	if err := s.store.UpsertUser(c.r.Context(), user); err != nil {
		return err
	}
	raw, err := newToken()
	if err != nil {
		return err
	}
	exp := now.Add(s.cfg.SessionTTL)
	if err := s.store.CreateSession(c.r.Context(), model.Session{
		ID: sessionID(raw), UserID: user.ID, GitHubToken: s.sess.seal(token),
		CreatedAt: now, ExpiresAt: exp, LastSeenAt: now,
	}); err != nil {
		return err
	}
	s.setSessionCookie(c.w, raw, exp)
	http.Redirect(c.w, c.r, safeRedirect(st.Redirect), http.StatusFound)
	return nil
}

func (s *Server) logout(c *call) error {
	if ck, err := c.r.Cookie(sessionCookie); err == nil {
		if token, ok := s.sess.parseCookie(ck.Value); ok {
			_ = s.store.DeleteSession(c.r.Context(), sessionID(token))
		}
	}
	s.clearSessionCookie(c.w)
	return c.noContent()
}

type userDTO struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

func (s *Server) getMe(c *call) error {
	u := c.actor.User
	return c.json(http.StatusOK, userDTO{ID: u.ID, Login: u.Login, Name: u.Name, AvatarURL: u.AvatarURL})
}

// GitHubOAuth implements OAuthProvider against github.com (or a fake server
// via the base URL fields).
type GitHubOAuth struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string // absolute URL of /api/v1/auth/callback
	WebBase      string // default https://github.com
	APIBase      string // default https://api.github.com
	HTTP         *http.Client
}

func (g *GitHubOAuth) web() string { return orDefault(g.WebBase, "https://github.com") }
func (g *GitHubOAuth) api() string { return orDefault(g.APIBase, "https://api.github.com") }
func (g *GitHubOAuth) client() *http.Client {
	if g.HTTP != nil {
		return g.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}
func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return strings.TrimRight(v, "/")
}

// AuthorizeURL implements OAuthProvider.
func (g *GitHubOAuth) AuthorizeURL(state string) string {
	v := url.Values{"client_id": {g.ClientID}, "redirect_uri": {g.RedirectURL}, "state": {state}}
	return g.web() + "/login/oauth/authorize?" + v.Encode()
}

// Exchange implements OAuthProvider.
func (g *GitHubOAuth) Exchange(ctx context.Context, code string) (string, error) {
	form := url.Values{"client_id": {g.ClientID}, "client_secret": {g.ClientSecret}, "code": {code}, "redirect_uri": {g.RedirectURL}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.web()+"/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := g.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("oauth: decode token response: %w", err)
	}
	switch {
	case out.Error == "bad_verification_code":
		return "", ErrBadCode
	case out.Error != "":
		return "", fmt.Errorf("oauth: github error %q", out.Error)
	case out.AccessToken == "":
		return "", ErrBadCode
	}
	return out.AccessToken, nil
}

// User implements OAuthProvider.
func (g *GitHubOAuth) User(ctx context.Context, token string) (model.User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.api()+"/user", nil)
	if err != nil {
		return model.User{}, err
	}
	setGitHubHeaders(req, token)
	resp, err := g.client().Do(req)
	if err != nil {
		return model.User{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return model.User{}, fmt.Errorf("oauth: GET /user: status %d", resp.StatusCode)
	}
	var out struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return model.User{}, err
	}
	return model.User{ID: out.ID, Login: out.Login, Name: out.Name, AvatarURL: out.AvatarURL}, nil
}

func setGitHubHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
}
