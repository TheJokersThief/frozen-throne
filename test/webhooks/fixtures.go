package webhooks

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"text/template"
)

//go:embed fixtures/*.json.tmpl
var fixtureFS embed.FS

// Delivery is one webhook delivery: the X-GitHub-Event name and the JSON body.
type Delivery struct {
	Event string
	Body  []byte
}

// Params fills the fixture templates. Zero values get sensible defaults:
// installation 1001 for account "acme" (Organization), repo acme/widgets
// (id 2001, default branch main), PR #1 with a fixed head SHA.
type Params struct {
	InstallationID int64
	AccountLogin   string
	AccountID      int64
	AccountType    string // "Organization" or "User"

	Owner         string
	Repo          string
	RepoID        int64
	DefaultBranch string
	Private       bool

	PRNumber int
	HeadSHA  string // PR head, check_run head or merge group head
	BaseSHA  string
	Draft    bool

	Sender string // login of the acting user
}

func (p Params) withDefaults() Params {
	if p.InstallationID == 0 {
		p.InstallationID = 1001
	}
	if p.AccountLogin == "" {
		p.AccountLogin = "acme"
	}
	if p.AccountID == 0 {
		p.AccountID = 3001
	}
	if p.AccountType == "" {
		p.AccountType = "Organization"
	}
	if p.Owner == "" {
		p.Owner = p.AccountLogin
	}
	if p.Repo == "" {
		p.Repo = "widgets"
	}
	if p.RepoID == 0 {
		p.RepoID = 2001
	}
	if p.DefaultBranch == "" {
		p.DefaultBranch = "main"
	}
	if p.PRNumber == 0 {
		p.PRNumber = 1
	}
	if p.HeadSHA == "" {
		p.HeadSHA = "1111111111111111111111111111111111111111"
	}
	if p.BaseSHA == "" {
		p.BaseSHA = "2222222222222222222222222222222222222222"
	}
	if p.Sender == "" {
		p.Sender = "octocat"
	}
	return p
}

// view is what templates see.
type view struct {
	Params
	Action string
	Merged bool
	State  string
}

type spec struct {
	event, file, action string
	merged              bool
	state               string
}

var specs = map[string]spec{
	"pull_request.opened":               {"pull_request", "pull_request", "opened", false, "open"},
	"pull_request.synchronize":          {"pull_request", "pull_request", "synchronize", false, "open"},
	"pull_request.reopened":             {"pull_request", "pull_request", "reopened", false, "open"},
	"pull_request.closed":               {"pull_request", "pull_request", "closed", false, "closed"},
	"pull_request.closed_merged":        {"pull_request", "pull_request", "closed", true, "closed"},
	"check_run.rerequested":             {"check_run", "check_run", "rerequested", false, ""},
	"merge_group.checks_requested":      {"merge_group", "merge_group", "checks_requested", false, ""},
	"installation.created":              {"installation", "installation", "created", false, ""},
	"installation.deleted":              {"installation", "installation", "deleted", false, ""},
	"installation_repositories.added":   {"installation_repositories", "installation_repositories", "added", false, ""},
	"installation_repositories.removed": {"installation_repositories", "installation_repositories", "removed", false, ""},
	"ping":                              {"ping", "ping", "", false, ""},
	"star.created":                      {"star", "star", "created", false, ""},
}

var tmpl = template.Must(template.New("").Funcs(template.FuncMap{
	"q": func(v any) (string, error) { b, err := json.Marshal(v); return string(b), err },
}).ParseFS(fixtureFS, "fixtures/*.json.tmpl"))

// Render renders a named fixture (e.g. "pull_request.opened"). Names are the
// keys of the constructors below.
func Render(name string, p Params) (Delivery, error) {
	s, ok := specs[name]
	if !ok {
		return Delivery{}, fmt.Errorf("webhooks: unknown fixture %q", name)
	}
	var buf bytes.Buffer
	v := view{Params: p.withDefaults(), Action: s.action, Merged: s.merged, State: s.state}
	if err := tmpl.ExecuteTemplate(&buf, s.file+".json.tmpl", v); err != nil {
		return Delivery{}, err
	}
	if !json.Valid(buf.Bytes()) {
		return Delivery{}, fmt.Errorf("webhooks: fixture %q rendered invalid JSON", name)
	}
	return Delivery{Event: s.event, Body: buf.Bytes()}, nil
}

func must(name string, p Params) Delivery {
	d, err := Render(name, p)
	if err != nil {
		panic(err)
	}
	return d
}

func PullRequestOpened(p Params) Delivery      { return must("pull_request.opened", p) }
func PullRequestSynchronize(p Params) Delivery { return must("pull_request.synchronize", p) }
func PullRequestReopened(p Params) Delivery    { return must("pull_request.reopened", p) }

// PullRequestClosed is a PR closed without merging.
func PullRequestClosed(p Params) Delivery { return must("pull_request.closed", p) }

// PullRequestMerged is a PR closed by merging.
func PullRequestMerged(p Params) Delivery { return must("pull_request.closed_merged", p) }

// CheckRunRerequested is a "frozen-throne" check run re-request for HeadSHA,
// associated with pull request PRNumber.
func CheckRunRerequested(p Params) Delivery { return must("check_run.rerequested", p) }

// MergeGroupChecksRequested has head_sha HeadSHA and a head_ref naming PRNumber.
func MergeGroupChecksRequested(p Params) Delivery { return must("merge_group.checks_requested", p) }

func InstallationCreated(p Params) Delivery { return must("installation.created", p) }
func InstallationDeleted(p Params) Delivery { return must("installation.deleted", p) }
func InstallationRepositoriesAdded(p Params) Delivery {
	return must("installation_repositories.added", p)
}
func InstallationRepositoriesRemoved(p Params) Delivery {
	return must("installation_repositories.removed", p)
}

func Ping(p Params) Delivery { return must("ping", p) }

// StarCreated is an event frozen-throne does not handle.
func StarCreated(p Params) Delivery { return must("star.created", p) }
