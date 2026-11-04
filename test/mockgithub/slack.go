package mockgithub

import (
	"io"
	"net/http"
)

// SlackPost is one message delivered to the fake Slack incoming webhook.
type SlackPost struct {
	Hook string `json:"hook"` // last path segment of the webhook URL
	Body string `json:"body"` // raw request body
}

// routesSlack registers a fake Slack incoming webhook under
// /_mock/slack/hooks/{hook}. The app is given that URL as an org's Slack
// webhook; the posts are read back through Client.SlackPosts.
func (s *Server) routesSlack() {
	s.mux.HandleFunc("POST /_mock/slack/hooks/{hook}", s.slackHook)
	s.mux.HandleFunc("GET /_mock/slack/posts", s.slackPosts)
}

func (s *Server) slackHook(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxRecordedBody))
	s.mu.Lock()
	s.st.slack = append(s.st.slack, SlackPost{Hook: r.PathValue("hook"), Body: string(body)})
	s.mu.Unlock()
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) slackPosts(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	out := append([]SlackPost{}, s.st.slack...)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}
