package mockgithub

import (
	"net/http"
	"sort"
	"strconv"
	"time"
)

func (s *Server) routesGitHub() {
	s.routesChecks()
	s.mux.HandleFunc("POST /app/installations/{id}/access_tokens", s.accessToken)
	s.mux.HandleFunc("GET /repos/{owner}/{repo}/pulls", s.listPulls)
}

const tokenTTL = time.Hour

func (s *Server) accessToken(w http.ResponseWriter, r *http.Request) {
	jwt := bearer(r)
	if jwt == "" {
		ghError(w, http.StatusUnauthorized, "A JSON web token could not be decoded")
		return
	}
	if err := verifyAppJWT(jwt, s.cfg.AppID, s.cfg.AppPublicKey, s.cfg.Now()); err != nil {
		ghError(w, http.StatusUnauthorized, "A JSON web token could not be decoded: "+err.Error())
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.st.installations[id]; err != nil || !ok {
		ghError(w, http.StatusNotFound, "Not Found")
		return
	}
	exp := s.cfg.Now().Add(tokenTTL)
	tok := "ghs_" + randomHex(20)
	s.st.instTokens[tok] = token{installationID: id, expires: exp.Unix()}
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":      tok,
		"expires_at": exp.UTC().Format(time.RFC3339),
	})
}

// installationRepo authorizes an installation token for the repo in the path.
// It writes the error response and returns false on failure. Caller holds mu.
func (s *Server) installationRepo(w http.ResponseWriter, r *http.Request) (Repo, int64, bool) {
	t, ok := s.st.instTokens[bearer(r)]
	if !ok || s.cfg.Now().Unix() >= t.expires {
		ghError(w, http.StatusUnauthorized, "Bad credentials")
		return Repo{}, 0, false
	}
	repo, ok := s.st.repos[repoKey(r.PathValue("owner"), r.PathValue("repo"))]
	if !ok || repo.InstallationID != t.installationID {
		ghError(w, http.StatusNotFound, "Not Found")
		return Repo{}, 0, false
	}
	return repo, t.installationID, true
}

func (s *Server) listPulls(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, _, ok := s.installationRepo(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	state := q.Get("state")
	if state == "" {
		state = "open"
	}
	perPage := atoiDefault(q.Get("per_page"), 30, 100)
	page := atoiDefault(q.Get("page"), 1, 1<<20)

	var match []PR
	for _, p := range s.st.pulls[repoKey(repo.Owner, repo.Name)] {
		if state == "all" || p.State == state {
			match = append(match, p)
		}
	}
	sort.Slice(match, func(i, j int) bool { return match[i].Number > match[j].Number })
	lo := (page - 1) * perPage
	if lo > len(match) {
		lo = len(match)
	}
	hi := lo + perPage
	if hi > len(match) {
		hi = len(match)
	}
	out := make([]map[string]any, 0, hi-lo)
	for _, p := range match[lo:hi] {
		out = append(out, pullJSON(repo, p))
	}
	writeJSON(w, http.StatusOK, out)
}

func pullJSON(repo Repo, p PR) map[string]any {
	return map[string]any{
		"number": p.Number,
		"state":  p.State,
		"draft":  p.Draft,
		"head":   map[string]any{"sha": p.HeadSHA, "ref": "pr-" + strconv.Itoa(p.Number)},
		"base":   map[string]any{"ref": repo.DefaultBranch},
	}
}

func atoiDefault(s string, def, max int) int {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return def
	}
	if n > max {
		return max
	}
	return n
}
