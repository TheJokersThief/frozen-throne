package mockgithub

import (
	"net/http"
	"net/url"
)

func (s *Server) routesOAuth() {
	s.mux.HandleFunc("GET /login/oauth/authorize", s.authorize)
	s.mux.HandleFunc("POST /login/oauth/access_token", s.exchange)
}

// newCode issues a single-use authorization code for login. Caller holds mu.
func (s *Server) newCode(login string) string {
	code := randomHex(10)
	s.st.codes[code] = login
	return code
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.ClientID != "" && q.Get("client_id") != s.cfg.ClientID {
		ghError(w, http.StatusNotFound, "The client_id is not valid")
		return
	}
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || !redirect.IsAbs() {
		ghError(w, http.StatusUnprocessableEntity, "redirect_uri must be absolute")
		return
	}
	login := q.Get("login")
	if login == "" {
		login = s.st.defaultUser
	}
	if _, ok := s.st.users[login]; !ok {
		ghError(w, http.StatusUnprocessableEntity, "no such mock user (seed one and set the default user)")
		return
	}
	rq := redirect.Query()
	rq.Set("code", s.newCode(login))
	if st := q.Get("state"); st != "" {
		rq.Set("state", st)
	}
	redirect.RawQuery = rq.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (s *Server) exchange(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		ghError(w, http.StatusBadRequest, "bad form")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if (s.cfg.ClientID != "" && r.PostForm.Get("client_id") != s.cfg.ClientID) ||
		(s.cfg.ClientSecret != "" && r.PostForm.Get("client_secret") != s.cfg.ClientSecret) {
		writeJSON(w, http.StatusOK, map[string]string{"error": "incorrect_client_credentials", "error_description": "The client_id and/or client_secret passed are incorrect."})
		return
	}
	code := r.PostForm.Get("code")
	login, ok := s.st.codes[code]
	if !ok {
		writeJSON(w, http.StatusOK, map[string]string{"error": "bad_verification_code", "error_description": "The code passed is incorrect or expired."})
		return
	}
	delete(s.st.codes, code)
	writeJSON(w, http.StatusOK, map[string]string{
		"access_token": s.st.users[login].Token, "token_type": "bearer", "scope": "",
	})
}
