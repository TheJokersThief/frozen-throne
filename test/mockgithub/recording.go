package mockgithub

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

const maxRecordedBody = 64 << 10

// statusWriter remembers the status code written.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// handle records every non-control request and applies injected faults.
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(io.LimitReader(r.Body, maxRecordedBody))
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
	}

	s.mu.Lock()
	s.st.callSeq++
	call := &Call{
		Seq:     s.st.callSeq,
		Method:  r.Method,
		Path:    r.URL.Path,
		Query:   r.URL.RawQuery,
		Headers: redactedHeaders(r),
		Body:    redactedBody(r, body),
	}
	s.st.calls = append(s.st.calls, call)
	fault := s.takeFault(r)
	s.mu.Unlock()

	sw := &statusWriter{ResponseWriter: w}
	if fault != nil {
		ghError(sw, fault.Status, "mock fault injected")
	} else {
		s.mux.ServeHTTP(sw, r)
	}

	s.mu.Lock()
	call.Status = sw.status
	s.mu.Unlock()
}

// takeFault consumes one use of the first matching fault. Caller holds mu.
func (s *Server) takeFault(r *http.Request) *Fault {
	for i, f := range s.st.faults {
		if (f.Method == "" || strings.EqualFold(f.Method, r.Method)) && strings.HasPrefix(r.URL.Path, f.PathPrefix) {
			hit := *f
			f.Count--
			if f.Count <= 0 {
				s.st.faults = append(s.st.faults[:i], s.st.faults[i+1:]...)
			}
			return &hit
		}
	}
	return nil
}

var recordedHeaders = []string{"Accept", "Content-Type", "User-Agent", "X-GitHub-Api-Version"}

func redactedHeaders(r *http.Request) map[string]string {
	out := map[string]string{}
	for _, k := range recordedHeaders {
		if v := r.Header.Get(k); v != "" {
			out[k] = v
		}
	}
	if h := r.Header.Get("Authorization"); h != "" {
		out["Authorization"] = redactAuth(h)
	}
	return out
}

// redactAuth keeps the scheme and the kind of credential, never its value.
func redactAuth(h string) string {
	scheme, cred, _ := strings.Cut(h, " ")
	kind := "user-token"
	switch {
	case strings.HasPrefix(cred, "ghs_"):
		kind = "installation-token"
	case strings.Count(cred, ".") == 2:
		kind = "app-jwt"
	}
	return scheme + " <" + kind + ">"
}

func redactedBody(r *http.Request, body []byte) string {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt == "application/x-www-form-urlencoded" {
		if v, err := url.ParseQuery(string(body)); err == nil {
			if v.Has("client_secret") {
				v.Set("client_secret", "[redacted]")
			}
			return v.Encode()
		}
	}
	return string(body)
}
