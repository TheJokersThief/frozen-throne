package mockgithub

import (
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Config configures a Server.
type Config struct {
	AppID        int64
	AppPublicKey *rsa.PublicKey
	ClientID     string // OAuth client id; unchecked when empty
	ClientSecret string // OAuth client secret; unchecked when empty
	Now          func() time.Time
}

// Server is the mock GitHub. Use New to build one.
type Server struct {
	cfg Config
	mux *http.ServeMux
	mu  sync.Mutex
	st  *state
}

// New returns the mock as an http.Handler.
func New(cfg Config) *Server {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Server{cfg: cfg, mux: http.NewServeMux(), st: newState()}
	s.routesGitHub()
	s.routesControl()
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func ghError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"message": msg})
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	for _, p := range []string{"Bearer ", "bearer ", "token "} {
		if strings.HasPrefix(h, p) {
			return strings.TrimSpace(h[len(p):])
		}
	}
	return ""
}
