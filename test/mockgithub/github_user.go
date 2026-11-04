package mockgithub

import (
	"net/http"
	"strings"
)

func (s *Server) routesUser() {
	s.mux.HandleFunc("GET /user", s.getUser)
	s.mux.HandleFunc("GET /repos/{owner}/{repo}", s.getRepo)
	s.mux.HandleFunc("GET /user/memberships/orgs/{org}", s.getMembership)
}

// userFor resolves the bearer user token. Caller holds mu.
func (s *Server) userFor(w http.ResponseWriter, r *http.Request) (*User, bool) {
	login, ok := s.st.userTokens[bearer(r)]
	if !ok {
		ghError(w, http.StatusUnauthorized, "Bad credentials")
		return nil, false
	}
	return s.st.users[login], true
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.userFor(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": u.ID, "login": u.Login, "name": u.Name,
		"avatar_url": "https://avatars.mock.invalid/u/" + u.Login,
	})
}

// permLevel orders GitHub's repository roles.
var permLevel = map[string]int{"pull": 1, "triage": 2, "push": 3, "maintain": 4, "admin": 5}

func (s *Server) getRepo(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.userFor(w, r)
	if !ok {
		return
	}
	repo, ok := s.st.repos[repoKey(r.PathValue("owner"), r.PathValue("repo"))]
	level := 0
	for k, v := range u.RepoPerms {
		if strings.EqualFold(k, repo.Owner+"/"+repo.Name) {
			level = permLevel[v]
		}
	}
	if !ok || level == 0 {
		ghError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": repo.ID, "name": repo.Name, "full_name": repo.Owner + "/" + repo.Name,
		"private": repo.Private, "default_branch": repo.DefaultBranch,
		"owner": map[string]any{"login": repo.Owner},
		"permissions": map[string]bool{
			"admin": level >= 5, "maintain": level >= 4, "push": level >= 3,
			"triage": level >= 2, "pull": level >= 1,
		},
	})
}

func (s *Server) getMembership(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.userFor(w, r)
	if !ok {
		return
	}
	org := r.PathValue("org")
	for k, role := range u.OrgRoles {
		if strings.EqualFold(k, org) {
			writeJSON(w, http.StatusOK, map[string]any{"state": "active", "role": role, "organization": map[string]any{"login": k}})
			return
		}
	}
	ghError(w, http.StatusNotFound, "Not Found")
}
