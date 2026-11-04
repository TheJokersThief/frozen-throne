package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func readNone(string) ([]byte, error) { return nil, nil }

func valid() map[string]string {
	return map[string]string{
		"FT_BASE_URL":              "https://freeze.example.com/",
		"FT_GITHUB_APP_ID":         "12345",
		"FT_GITHUB_PRIVATE_KEY":    "-----BEGIN-----pem",
		"FT_GITHUB_WEBHOOK_SECRET": "whsec",
		"FT_GITHUB_CLIENT_ID":      "cid",
		"FT_GITHUB_CLIENT_SECRET":  "csecret",
		"FT_SESSION_SECRET":        strings.Repeat("s", 32),
	}
}

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(valid()), readNone)
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "https://freeze.example.com" {
		t.Errorf("BaseURL = %q", c.BaseURL)
	}
	if c.ListenAddr != ":8080" || c.DBPath != "frozen-throne.db" || c.ReconcileInterval != 5*time.Minute {
		t.Errorf("defaults: %+v", c)
	}
	if !c.SecureCookies {
		t.Error("https base URL must imply secure cookies")
	}
	if c.AppID != 12345 || string(c.PrivateKeyPEM) != "-----BEGIN-----pem" {
		t.Errorf("app: %+v", c)
	}
	if c.RedirectURL() != "https://freeze.example.com/api/v1/auth/callback" {
		t.Errorf("RedirectURL = %q", c.RedirectURL())
	}
}

func TestHTTPBaseURLNotSecure(t *testing.T) {
	m := valid()
	m["FT_BASE_URL"] = "http://localhost:8080"
	c, err := Load(env(m), readNone)
	if err != nil || c.SecureCookies {
		t.Fatalf("err=%v secure=%v", err, c.SecureCookies)
	}
}

func TestOverrides(t *testing.T) {
	m := valid()
	m["FT_LISTEN_ADDR"] = "127.0.0.1:9000"
	m["FT_DB_PATH"] = "/data/x.db"
	m["FT_RECONCILE_INTERVAL"] = "30s"
	c, err := Load(env(m), readNone)
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != "127.0.0.1:9000" || c.DBPath != "/data/x.db" || c.ReconcileInterval != 30*time.Second {
		t.Errorf("%+v", c)
	}
}

func TestKeyAndSecretsFromFiles(t *testing.T) {
	m := valid()
	delete(m, "FT_GITHUB_PRIVATE_KEY")
	delete(m, "FT_SESSION_SECRET")
	m["FT_GITHUB_PRIVATE_KEY_FILE"] = "/run/secrets/key"
	m["FT_SESSION_SECRET_FILE"] = "/run/secrets/session"
	read := func(p string) ([]byte, error) {
		switch p {
		case "/run/secrets/key":
			return []byte("PEM\n"), nil
		case "/run/secrets/session":
			return []byte(strings.Repeat("z", 40) + "\n"), nil
		}
		return nil, nil
	}
	c, err := Load(env(m), read)
	if err != nil {
		t.Fatal(err)
	}
	if string(c.PrivateKeyPEM) != "PEM" || string(c.SessionSecret) != strings.Repeat("z", 40) {
		t.Errorf("%q %q", c.PrivateKeyPEM, c.SessionSecret)
	}
}

func TestEscapedNewlinesInKey(t *testing.T) {
	m := valid()
	m["FT_GITHUB_PRIVATE_KEY"] = `-----BEGIN-----\nabc\n-----END-----`
	c, err := Load(env(m), readNone)
	if err != nil {
		t.Fatal(err)
	}
	if string(c.PrivateKeyPEM) != "-----BEGIN-----\nabc\n-----END-----" {
		t.Errorf("%q", c.PrivateKeyPEM)
	}
}

func TestValidationErrorsListEverythingMissing(t *testing.T) {
	_, err := Load(env(map[string]string{}), readNone)
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"FT_BASE_URL", "FT_GITHUB_APP_ID", "FT_GITHUB_PRIVATE_KEY", "FT_GITHUB_WEBHOOK_SECRET", "FT_GITHUB_CLIENT_ID", "FT_GITHUB_CLIENT_SECRET", "FT_SESSION_SECRET"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s: %v", want, err)
		}
	}
}

func TestInvalidValues(t *testing.T) {
	cases := map[string]string{
		"FT_GITHUB_APP_ID":      "abc",
		"FT_SESSION_SECRET":     "short",
		"FT_BASE_URL":           "not a url",
		"FT_RECONCILE_INTERVAL": "soon",
	}
	for k, v := range cases {
		m := valid()
		m[k] = v
		if _, err := Load(env(m), readNone); err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s=%q: err=%v", k, v, err)
		}
	}
}

func TestBaseURLWithPathRejected(t *testing.T) {
	m := valid()
	m["FT_BASE_URL"] = "https://x.example.com/foo"
	if _, err := Load(env(m), readNone); err == nil || !strings.Contains(err.Error(), "FT_BASE_URL") {
		t.Fatalf("err=%v", err)
	}
}
