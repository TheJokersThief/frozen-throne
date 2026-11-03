package api

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHealthz(t *testing.T) {
	e := newEnv(t)
	e.expect(e.do(nil, "GET", "/healthz", nil), 200)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Fatalf("root healthz = %d", w.Code)
	}
}

func TestAuthRequiredAndCSRF(t *testing.T) {
	e := newEnv(t)
	e.expect(e.do(nil, "GET", "/auth/me", nil), 401)
	e.expect(e.do(nil, "GET", "/repos", nil), 401)

	// Mutation without the CSRF header is rejected even when logged in.
	bob := e.login("bob")
	req := httptest.NewRequest("POST", Prefix+"/repos/acme/web/freeze", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(bob)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("missing csrf header: %d", w.Code)
	}
	// Wrong content type with a body.
	req = httptest.NewRequest("POST", Prefix+"/repos/acme/web/freeze", strings.NewReader(`reason=x`))
	req.Header.Set(csrfHeader, csrfValue)
	req.Header.Set("Content-Type", "text/plain")
	req.AddCookie(bob)
	w = httptest.NewRecorder()
	e.h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("bad content type: %d", w.Code)
	}
}

func TestTamperedAndExpiredSession(t *testing.T) {
	e := newEnv(t)
	c := e.login("alice")
	bad := &http.Cookie{Name: c.Name, Value: c.Value[:len(c.Value)-2] + "AA"}
	e.expect(e.do(bad, "GET", "/auth/me", nil), 401)
	e.expect(e.do(&http.Cookie{Name: c.Name, Value: "junk"}, "GET", "/auth/me", nil), 401)

	e.expect(e.do(c, "GET", "/auth/me", nil), 200)
	e.srv.cfg.Now = func() time.Time { return e.now.Add(2 * time.Hour) }
	e.expect(e.do(c, "GET", "/auth/me", nil), 401)
}

func TestOAuthFlow(t *testing.T) {
	e := newEnv(t)
	r := e.expect(e.do(nil, "GET", "/auth/login?redirect=/repos/acme/web", nil), 302)
	loc, _ := url.Parse(r.Header().Get("Location"))
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatal("no state in redirect")
	}
	var stateCk *http.Cookie
	for _, c := range r.Result().Cookies() {
		if c.Name == stateCookie {
			stateCk = c
		}
	}
	if stateCk == nil || !stateCk.HttpOnly {
		t.Fatal("state cookie missing or not HttpOnly")
	}

	cb := func(code, st string, ck *http.Cookie) resp {
		req := httptest.NewRequest("GET", Prefix+"/auth/callback?code="+code+"&state="+url.QueryEscape(st), nil)
		if ck != nil {
			req.AddCookie(ck)
		}
		w := httptest.NewRecorder()
		e.h.ServeHTTP(w, req)
		return resp{w}
	}
	// Failures: bad code, forged state, missing browser binding.
	e.expect(cb("bad", state, stateCk), 400)
	e.expect(cb("good", state+"x", stateCk), 400)
	e.expect(cb("good", state, nil), 400)
	e.expect(cb("good", state, &http.Cookie{Name: stateCookie, Value: "other"}), 400)

	ok := e.expect(cb("good", state, stateCk), 302)
	if ok.Header().Get("Location") != "/repos/acme/web" {
		t.Fatalf("redirect = %q", ok.Header().Get("Location"))
	}
	var sess *http.Cookie
	for _, c := range ok.Result().Cookies() {
		if c.Name == sessionCookie {
			sess = c
		}
	}
	if sess == nil || !sess.HttpOnly || sess.SameSite != http.SameSiteLaxMode {
		t.Fatalf("bad session cookie: %+v", sess)
	}
	var me userDTO
	e.expect(e.do(sess, "GET", "/auth/me", nil), 200).decode(&me)
	if me.Login != "alice" {
		t.Fatalf("me = %+v", me)
	}

	// The stored token is encrypted, and the stored ID is not the cookie secret.
	tok, _ := e.srv.sess.parseCookie(sess.Value)
	s, err := e.st.GetSession(t.Context(), sessionID(tok), e.now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(s.GitHubToken), "gh-token") {
		t.Fatal("github token stored in clear")
	}

	e.expect(e.do(sess, "POST", "/auth/logout", nil), 204)
	e.expect(e.do(sess, "GET", "/auth/me", nil), 401)
	e.expect(e.do(nil, "POST", "/auth/logout", nil), 204) // idempotent
}

func TestSafeRedirect(t *testing.T) {
	for in, want := range map[string]string{
		"": "/", "/ok": "/ok", "/a?b=c": "/a?b=c", "//evil.com": "/", "https://evil.com": "/",
		"evil": "/", `/\evil.com`: "/", "/x\r\nSet-Cookie: a=b": "/",
	} {
		if got := safeRedirect(in); got != want {
			t.Errorf("safeRedirect(%q) = %q, want %q", in, got, want)
		}
	}
}
