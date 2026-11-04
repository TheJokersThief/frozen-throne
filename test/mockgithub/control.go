package mockgithub

import (
	"encoding/json"
	"net/http"
	"strings"
)

func (s *Server) routesControl() {
	s.mux.HandleFunc("POST /_mock/reset", s.ctlReset)
	s.mux.HandleFunc("POST /_mock/installations", s.ctlInstallation)
	s.mux.HandleFunc("POST /_mock/repos", s.ctlRepo)
	s.mux.HandleFunc("POST /_mock/pulls", s.ctlPull)
	s.mux.HandleFunc("GET /_mock/check-runs", s.ctlCheckRuns)
	s.mux.HandleFunc("POST /_mock/users", s.ctlUser)
	s.mux.HandleFunc("PUT /_mock/default-user", s.ctlDefaultUser)
	s.mux.HandleFunc("GET /_mock/calls", s.ctlCalls)
	s.mux.HandleFunc("DELETE /_mock/calls", s.ctlClearCalls)
	s.mux.HandleFunc("POST /_mock/faults", s.ctlFault)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		ghError(w, http.StatusBadRequest, "bad json: "+err.Error())
		return false
	}
	return true
}

func (s *Server) ctlReset(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.st = newState()
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ctlInstallation(w http.ResponseWriter, r *http.Request) {
	var in Installation
	if !decode(w, r, &in) {
		return
	}
	if in.AccountType == "" {
		in.AccountType = "Organization"
	}
	s.mu.Lock()
	s.st.installations[in.ID] = in
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) ctlRepo(w http.ResponseWriter, r *http.Request) {
	var in Repo
	if !decode(w, r, &in) {
		return
	}
	if in.DefaultBranch == "" {
		in.DefaultBranch = "main"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.st.installations[in.InstallationID]; !ok {
		ghError(w, http.StatusUnprocessableEntity, "unknown installation")
		return
	}
	s.st.repos[repoKey(in.Owner, in.Name)] = in
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) ctlPull(w http.ResponseWriter, r *http.Request) {
	var in PR
	if !decode(w, r, &in) {
		return
	}
	if in.State == "" {
		in.State = "open"
	}
	key := repoKey(in.Owner, in.Repo)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.st.repos[key]; !ok {
		ghError(w, http.StatusUnprocessableEntity, "unknown repo "+strings.ToLower(key))
		return
	}
	list := s.st.pulls[key]
	for i := range list {
		if list[i].Number == in.Number {
			list[i] = in
			writeJSON(w, http.StatusCreated, in)
			return
		}
	}
	s.st.pulls[key] = append(list, in)
	writeJSON(w, http.StatusCreated, in)
}

func (s *Server) ctlCheckRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []CheckRun{}
	for _, cr := range s.st.checkRuns {
		if o := q.Get("owner"); o != "" && !strings.EqualFold(o, cr.Owner) {
			continue
		}
		if n := q.Get("repo"); n != "" && !strings.EqualFold(n, cr.Repo) {
			continue
		}
		if sha := q.Get("head_sha"); sha != "" && sha != cr.HeadSHA {
			continue
		}
		if name := q.Get("name"); name != "" && name != cr.Name {
			continue
		}
		out = append(out, cr)
	}
	writeJSON(w, http.StatusOK, out)
}

// ctlUser creates or replaces a user (by login) and returns an OAuth code
// that is good for one token exchange.
func (s *Server) ctlUser(w http.ResponseWriter, r *http.Request) {
	var in User
	if !decode(w, r, &in) {
		return
	}
	if in.Login == "" {
		ghError(w, http.StatusUnprocessableEntity, "login is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.st.users[in.Login]; ok {
		delete(s.st.userTokens, old.Token)
	}
	if in.Token == "" {
		in.Token = "gho_" + randomHex(20)
	}
	s.st.users[in.Login] = &in
	s.st.userTokens[in.Token] = in.Login
	writeJSON(w, http.StatusCreated, map[string]string{"code": s.newCode(in.Login), "token": in.Token})
}

func (s *Server) ctlDefaultUser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Login string `json:"login"`
	}
	if !decode(w, r, &in) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.st.users[in.Login]; !ok {
		ghError(w, http.StatusUnprocessableEntity, "unknown user "+in.Login)
		return
	}
	s.st.defaultUser = in.Login
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ctlCalls(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Call, 0, len(s.st.calls))
	for _, c := range s.st.calls {
		out = append(out, *c)
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) ctlClearCalls(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.st.calls = nil
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) ctlFault(w http.ResponseWriter, r *http.Request) {
	var in Fault
	if !decode(w, r, &in) {
		return
	}
	if in.Status < 100 || in.Status > 599 || in.PathPrefix == "" {
		ghError(w, http.StatusUnprocessableEntity, "status and path_prefix are required")
		return
	}
	if in.Count < 1 {
		in.Count = 1
	}
	s.mu.Lock()
	s.st.faults = append(s.st.faults, &in)
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, in)
}
