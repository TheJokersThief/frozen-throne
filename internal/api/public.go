package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
)

type publicStatusDTO struct {
	Repo   string     `json:"repo"`
	Frozen bool       `json:"frozen"`
	Since  *time.Time `json:"since,omitempty"`
	Until  *time.Time `json:"until,omitempty"`
}

// publicRepo resolves an opted-in repo. Unknown, removed and non-opted-in
// repos are indistinguishable (model.ErrNotFound).
func (s *Server) publicState(c *call) (publicStatusDTO, error) {
	ctx := c.r.Context()
	repo, err := s.store.GetRepoByName(ctx, c.r.PathValue("owner"), c.r.PathValue("repo"))
	if err != nil {
		return publicStatusDTO{}, err
	}
	if repo.RemovedAt != nil || !repo.PublicStatus {
		return publicStatusDTO{}, model.ErrNotFound
	}
	dec, state, err := s.repoState(ctx, s.store, repo)
	if err != nil {
		return publicStatusDTO{}, err
	}
	out := publicStatusDTO{Repo: fullName(repo), Frozen: dec.Frozen}
	if !dec.Frozen {
		return out, nil
	}
	allEnd := true
	for _, r := range state.Reasons {
		if out.Since == nil || r.Since.Before(*out.Since) {
			since := r.Since
			out.Since = &since
		}
		if r.Until == nil {
			allEnd = false
		} else if out.Until == nil || r.Until.Before(*out.Until) {
			until := *r.Until
			out.Until = &until
		}
	}
	if !allEnd {
		out.Until = nil // a manual freeze has no known end
	}
	return out, nil
}

func (s *Server) getPublicStatus(c *call) error {
	st, err := s.publicState(c)
	if err != nil {
		return err // mapped to the generic 404 / 500
	}
	return c.json(http.StatusOK, st)
}

func (s *Server) getPublicBadge(c *call) error {
	st, err := s.publicState(c)
	status, value, color := http.StatusOK, "open", "#4c1"
	switch {
	case errors.Is(err, model.ErrNotFound):
		status, value, color = http.StatusNotFound, "unknown", "#9f9f9f"
	case err != nil:
		return err
	case st.Frozen:
		value, color = "frozen", "#e05d44"
	}
	c.w.Header().Set("Content-Type", "image/svg+xml")
	c.w.Header().Set("Cache-Control", "public, max-age=60")
	c.w.WriteHeader(status)
	_, _ = fmt.Fprint(c.w, badgeSVG("merge freeze", value, color))
	return nil
}

func badgeSVG(label, value, color string) string {
	lw, vw := 7*len(label)+10, 7*len(value)+10
	w := lw + vw
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%[1]d" height="20" role="img" aria-label="%[4]s: %[5]s">`+
		`<linearGradient id="s" x2="0" y2="100%%"><stop offset="0" stop-color="#bbb" stop-opacity=".1"/><stop offset="1" stop-opacity=".1"/></linearGradient>`+
		`<clipPath id="r"><rect width="%[1]d" height="20" rx="3" fill="#fff"/></clipPath>`+
		`<g clip-path="url(#r)"><rect width="%[2]d" height="20" fill="#555"/><rect x="%[2]d" width="%[3]d" height="20" fill="%[6]s"/><rect width="%[1]d" height="20" fill="url(#s)"/></g>`+
		`<g fill="#fff" text-anchor="middle" font-family="Verdana,Geneva,DejaVu Sans,sans-serif" font-size="11">`+
		`<text x="%[7]d" y="14">%[4]s</text><text x="%[8]d" y="14">%[5]s</text></g></svg>`,
		w, lw, vw, label, value, color, lw/2, lw+vw/2)
}
