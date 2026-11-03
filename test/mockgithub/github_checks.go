package mockgithub

import (
	"net/http"
	"strconv"
	"time"
)

func (s *Server) routesChecks() {
	s.mux.HandleFunc("POST /repos/{owner}/{repo}/check-runs", s.createCheckRun)
	s.mux.HandleFunc("PATCH /repos/{owner}/{repo}/check-runs/{id}", s.updateCheckRun)
}

type checkRunIn struct {
	Name        *string `json:"name"`
	HeadSHA     *string `json:"head_sha"`
	Status      *string `json:"status"`
	Conclusion  *string `json:"conclusion"`
	CompletedAt *string `json:"completed_at"`
	Output      *struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	} `json:"output"`
}

func (in checkRunIn) apply(cr *CheckRun) {
	if in.Name != nil {
		cr.Name = *in.Name
	}
	if in.HeadSHA != nil {
		cr.HeadSHA = *in.HeadSHA
	}
	if in.Status != nil {
		cr.Status = *in.Status
	}
	if in.Conclusion != nil {
		cr.Conclusion = *in.Conclusion
	}
	if in.CompletedAt != nil {
		cr.CompletedAt = *in.CompletedAt
	}
	if in.Output != nil {
		cr.Output = CheckRunOutput{Title: in.Output.Title, Summary: in.Output.Summary}
	}
}

func checkRunJSON(cr CheckRun) map[string]any {
	return map[string]any{
		"id":           cr.ID,
		"name":         cr.Name,
		"head_sha":     cr.HeadSHA,
		"status":       cr.Status,
		"conclusion":   cr.Conclusion,
		"completed_at": cr.CompletedAt,
		"output":       map[string]any{"title": cr.Output.Title, "summary": cr.Output.Summary},
	}
}

func (s *Server) createCheckRun(w http.ResponseWriter, r *http.Request) {
	var in checkRunIn
	if !decode(w, r, &in) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, instID, ok := s.installationRepo(w, r)
	if !ok {
		return
	}
	if in.Name == nil || *in.Name == "" || in.HeadSHA == nil || *in.HeadSHA == "" {
		ghError(w, http.StatusUnprocessableEntity, "Validation Failed: name and head_sha are required")
		return
	}
	s.st.nextCheckID++
	cr := CheckRun{ID: s.st.nextCheckID, InstallationID: instID, Owner: repo.Owner, Repo: repo.Name, Status: "queued"}
	in.apply(&cr)
	// GitHub: a conclusion without a status completes the run.
	if in.Conclusion != nil && in.Status == nil {
		cr.Status = "completed"
	}
	if cr.Status == "completed" && cr.CompletedAt == "" {
		cr.CompletedAt = s.cfg.Now().UTC().Format(time.RFC3339)
	}
	s.st.checkRuns = append(s.st.checkRuns, cr)
	writeJSON(w, http.StatusCreated, checkRunJSON(cr))
}

func (s *Server) updateCheckRun(w http.ResponseWriter, r *http.Request) {
	var in checkRunIn
	if !decode(w, r, &in) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	s.mu.Lock()
	defer s.mu.Unlock()
	repo, _, ok := s.installationRepo(w, r)
	if !ok {
		return
	}
	for i := range s.st.checkRuns {
		cr := &s.st.checkRuns[i]
		if err == nil && cr.ID == id && repoKey(cr.Owner, cr.Repo) == repoKey(repo.Owner, repo.Name) {
			in.apply(cr)
			writeJSON(w, http.StatusOK, checkRunJSON(*cr))
			return
		}
	}
	ghError(w, http.StatusNotFound, "Not Found")
}
