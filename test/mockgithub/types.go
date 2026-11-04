package mockgithub

// Installation is a GitHub App installation on an account.
type Installation struct {
	ID           int64  `json:"id"`
	AccountLogin string `json:"account_login"`
	AccountID    int64  `json:"account_id"`
	AccountType  string `json:"account_type"` // "Organization" or "User"
}

// Repo is a repository reachable through an installation.
type Repo struct {
	InstallationID int64  `json:"installation_id"`
	ID             int64  `json:"id"`
	Owner          string `json:"owner"`
	Name           string `json:"name"`
	DefaultBranch  string `json:"default_branch"`
	Private        bool   `json:"private"`
}

// PR is a seeded pull request.
type PR struct {
	Owner   string `json:"owner"`
	Repo    string `json:"repo"`
	Number  int    `json:"number"`
	HeadSHA string `json:"head_sha"`
	Draft   bool   `json:"draft"`
	State   string `json:"state"` // "open" (default) or "closed"
}

// CheckRunOutput is the output block of a check run.
type CheckRunOutput struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

// CheckRun is a check run as recorded by the mock.
type CheckRun struct {
	ID             int64          `json:"id"`
	InstallationID int64          `json:"installation_id"`
	Owner          string         `json:"owner"`
	Repo           string         `json:"repo"`
	Name           string         `json:"name"`
	HeadSHA        string         `json:"head_sha"`
	Status         string         `json:"status"`
	Conclusion     string         `json:"conclusion"`
	CompletedAt    string         `json:"completed_at"`
	Output         CheckRunOutput `json:"output"`
}

// User is a GitHub user who can sign in through the mock OAuth flow.
type User struct {
	ID        int64             `json:"id"`
	Login     string            `json:"login"`
	Name      string            `json:"name"`
	Token     string            `json:"token,omitempty"`      // OAuth access token; generated if empty
	RepoPerms map[string]string `json:"repo_perms,omitempty"` // "owner/name" -> admin|maintain|push|triage|pull
	OrgRoles  map[string]string `json:"org_roles,omitempty"`  // org login -> admin|member
}

// Call is one recorded request to the REST/OAuth surface.
type Call struct {
	Seq     int               `json:"seq"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Query   string            `json:"query"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// Fault makes the next Count matching requests fail with Status.
type Fault struct {
	Method     string `json:"method,omitempty"` // empty matches any
	PathPrefix string `json:"path_prefix"`
	Status     int    `json:"status"`
	Count      int    `json:"count"`
}
