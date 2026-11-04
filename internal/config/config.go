// Package config reads the server's settings from environment variables.
// Secrets may alternatively be supplied as files (<NAME>_FILE) for Docker and
// Kubernetes secrets.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Config is the validated server configuration.
type Config struct {
	BaseURL           string // public origin, no trailing slash
	ListenAddr        string
	DBPath            string
	ReconcileInterval time.Duration
	SecureCookies     bool

	AppID         int64
	PrivateKeyPEM []byte
	WebhookSecret []byte
	ClientID      string
	ClientSecret  string
	SessionSecret []byte
}

// RedirectURL is the OAuth callback URL registered on the GitHub App.
func (c Config) RedirectURL() string { return c.BaseURL + "/api/v1/auth/callback" }

// Load builds a Config. getenv and readFile are injected for tests; every
// problem is reported at once so a first-time setup is a single round trip.
func Load(getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error) {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	// value returns NAME, or the contents of NAME_FILE (trailing newline trimmed).
	value := func(name string) string {
		if v := getenv(name); v != "" {
			return v
		}
		if f := getenv(name + "_FILE"); f != "" {
			b, err := readFile(f)
			if err != nil {
				bad("%s_FILE: %v", name, err)
				return ""
			}
			return strings.TrimRight(string(b), "\r\n")
		}
		return ""
	}
	def := func(name, d string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return d
	}

	c := Config{
		ListenAddr: def("FT_LISTEN_ADDR", ":8080"),
		DBPath:     def("FT_DB_PATH", "frozen-throne.db"),
	}

	base := strings.TrimRight(getenv("FT_BASE_URL"), "/")
	if base == "" {
		bad("FT_BASE_URL is required (public URL of this server, e.g. https://freeze.example.com)")
	} else if u, err := url.Parse(base); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Path != "" {
		bad("FT_BASE_URL must be an absolute http(s) origin without a path, got %q", base)
	} else {
		c.BaseURL = base
		c.SecureCookies = u.Scheme == "https"
	}

	c.ReconcileInterval = 5 * time.Minute
	if v := getenv("FT_RECONCILE_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			bad("FT_RECONCILE_INTERVAL: %v", err)
		}
		c.ReconcileInterval = d
	}

	if v := getenv("FT_GITHUB_APP_ID"); v == "" {
		bad("FT_GITHUB_APP_ID is required")
	} else if id, err := strconv.ParseInt(v, 10, 64); err != nil || id <= 0 {
		bad("FT_GITHUB_APP_ID must be a positive integer, got %q", v)
	} else {
		c.AppID = id
	}

	if k := value("FT_GITHUB_PRIVATE_KEY"); k == "" {
		bad("FT_GITHUB_PRIVATE_KEY (or FT_GITHUB_PRIVATE_KEY_FILE) is required")
	} else {
		// Allow single-line env values with literal \n sequences.
		if !strings.Contains(k, "\n") {
			k = strings.ReplaceAll(k, `\n`, "\n")
		}
		c.PrivateKeyPEM = []byte(k)
	}

	if s := value("FT_GITHUB_WEBHOOK_SECRET"); s == "" {
		bad("FT_GITHUB_WEBHOOK_SECRET (or _FILE) is required")
	} else {
		c.WebhookSecret = []byte(s)
	}
	if c.ClientID = getenv("FT_GITHUB_CLIENT_ID"); c.ClientID == "" {
		bad("FT_GITHUB_CLIENT_ID is required")
	}
	if c.ClientSecret = value("FT_GITHUB_CLIENT_SECRET"); c.ClientSecret == "" {
		bad("FT_GITHUB_CLIENT_SECRET (or _FILE) is required")
	}

	if s := value("FT_SESSION_SECRET"); s == "" {
		bad("FT_SESSION_SECRET (or _FILE) is required (at least 32 characters, e.g. `openssl rand -hex 32`)")
	} else if len(s) < 32 {
		bad("FT_SESSION_SECRET must be at least 32 characters, got %d", len(s))
	} else {
		c.SessionSecret = []byte(s)
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	return c, nil
}
