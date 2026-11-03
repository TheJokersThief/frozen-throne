package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
)

// maxWebhookBody is GitHub's documented payload cap (25 MB).
const maxWebhookBody = 25 << 20

// VerifySignature checks an X-Hub-Signature-256 header ("sha256=<hex>")
// against body using the shared secret, in constant time.
func VerifySignature(secret, body []byte, header string) bool {
	hexSig, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(hexSig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// ServeHTTP is the webhook endpoint. It verifies the signature, then handles
// the event synchronously (GitHub allows 10s) so that a failure surfaces as a
// failed delivery that can be redelivered. Unknown events are acknowledged.
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		http.Error(w, "bad body", http.StatusRequestEntityTooLarge)
		return
	}
	if !VerifySignature(s.secret, body, r.Header.Get("X-Hub-Signature-256")) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 9*time.Second)
	defer cancel()
	if err := s.HandleEvent(ctx, event, body); err != nil {
		if errors.Is(err, errBadPayload) {
			http.Error(w, "bad payload", http.StatusBadRequest)
			return
		}
		s.log.Error("webhook", "event", event, "delivery", r.Header.Get("X-GitHub-Delivery"), "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

var errBadPayload = errors.New("bad payload")

type repoPayload struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	DefaultBranch string `json:"default_branch"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

type installationPayload struct {
	ID          int64      `json:"id"`
	SuspendedAt *time.Time `json:"suspended_at"`
	Account     struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
		Type  string `json:"type"`
	} `json:"account"`
}

type envelope struct {
	Action       string              `json:"action"`
	Installation installationPayload `json:"installation"`
	Repository   repoPayload         `json:"repository"`

	PullRequest struct {
		Number int    `json:"number"`
		Draft  bool   `json:"draft"`
		Merged bool   `json:"merged"`
		State  string `json:"state"`
		Head   struct {
			SHA string `json:"sha"`
		} `json:"head"`
	} `json:"pull_request"`

	CheckRun struct {
		Name    string `json:"name"`
		HeadSHA string `json:"head_sha"`
		PRs     []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_run"`

	MergeGroup struct {
		HeadSHA string `json:"head_sha"`
		HeadRef string `json:"head_ref"`
	} `json:"merge_group"`

	// installation: repositories; installation_repositories: *_added/removed.
	Repositories        []repoPayload `json:"repositories"`
	RepositoriesAdded   []repoPayload `json:"repositories_added"`
	RepositoriesRemoved []repoPayload `json:"repositories_removed"`
}

// HandleEvent processes one verified webhook delivery.
func (s *Service) HandleEvent(ctx context.Context, event string, body []byte) error {
	var e envelope
	switch event {
	case "pull_request", "check_run", "merge_group", "installation", "installation_repositories":
		if err := json.Unmarshal(body, &e); err != nil {
			return fmt.Errorf("%w: %v", errBadPayload, err)
		}
	default:
		return nil // ping and everything else
	}
	switch event {
	case "installation":
		return s.onInstallation(ctx, e)
	case "installation_repositories":
		return s.onInstallationRepos(ctx, e)
	case "pull_request":
		return s.onPullRequest(ctx, e)
	case "check_run":
		return s.onCheckRun(ctx, e)
	case "merge_group":
		return s.onMergeGroup(ctx, e)
	}
	return nil
}

// upsertInstallation records the installation from a payload.
func (s *Service) upsertInstallation(ctx context.Context, ip installationPayload) error {
	if ip.ID == 0 {
		return fmt.Errorf("%w: missing installation id", errBadPayload)
	}
	return s.st.UpsertInstallation(ctx, model.Installation{
		ID: ip.ID, AccountLogin: ip.Account.Login, AccountID: ip.Account.ID, AccountType: ip.Account.Type,
		SuspendedAt: ip.SuspendedAt, UpdatedAt: s.now(),
	})
}

// upsertRepo records a repo from a payload. Installation payloads carry no
// default branch or archived flag, so existing values are kept when the
// payload leaves them empty.
func (s *Service) upsertRepo(ctx context.Context, instID int64, rp repoPayload) (model.Repo, error) {
	owner, name := rp.Owner.Login, rp.Name
	if owner == "" || name == "" {
		if o, n, ok := strings.Cut(rp.FullName, "/"); ok {
			owner, name = o, n
		}
	}
	if rp.ID == 0 || owner == "" || name == "" {
		return model.Repo{}, fmt.Errorf("%w: incomplete repository", errBadPayload)
	}
	repo := model.Repo{
		ID: rp.ID, InstallationID: instID, Owner: owner, Name: name,
		DefaultBranch: rp.DefaultBranch, Private: rp.Private, Archived: rp.Archived, UpdatedAt: s.now(),
	}
	if old, err := s.st.GetRepo(ctx, rp.ID); err == nil {
		if repo.DefaultBranch == "" {
			repo.DefaultBranch = old.DefaultBranch
		}
		if rp.DefaultBranch == "" { // thin payload: archived flag is not meaningful
			repo.Archived = old.Archived
		}
		repo.PublicStatus = old.PublicStatus
	} else if !errors.Is(err, model.ErrNotFound) {
		return model.Repo{}, err
	}
	if err := s.st.UpsertRepo(ctx, repo); err != nil {
		return model.Repo{}, err
	}
	return repo, nil
}

func (s *Service) onInstallation(ctx context.Context, e envelope) error {
	id := e.Installation.ID
	switch e.Action {
	case "deleted":
		if id == 0 {
			return fmt.Errorf("%w: missing installation id", errBadPayload)
		}
		err := s.st.DeleteInstallation(ctx, id)
		if errors.Is(err, model.ErrNotFound) {
			return nil
		}
		return err
	case "created", "unsuspend", "suspend", "new_permissions_accepted":
		ip := e.Installation
		if e.Action == "unsuspend" {
			ip.SuspendedAt = nil
		}
		if e.Action == "suspend" && ip.SuspendedAt == nil {
			t := s.now()
			ip.SuspendedAt = &t
		}
		if err := s.upsertInstallation(ctx, ip); err != nil {
			return err
		}
		for _, rp := range e.Repositories {
			if _, err := s.upsertRepo(ctx, id, rp); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) onInstallationRepos(ctx context.Context, e envelope) error {
	id := e.Installation.ID
	if err := s.upsertInstallation(ctx, e.Installation); err != nil {
		return err
	}
	for _, rp := range e.RepositoriesAdded {
		if _, err := s.upsertRepo(ctx, id, rp); err != nil {
			return err
		}
	}
	if len(e.RepositoriesRemoved) > 0 {
		ids := make([]int64, 0, len(e.RepositoriesRemoved))
		for _, rp := range e.RepositoriesRemoved {
			ids = append(ids, rp.ID)
		}
		if err := s.st.MarkReposRemoved(ctx, id, ids, s.now()); err != nil {
			return err
		}
	}
	return nil
}

// repoForEvent returns the stored repo for an event, creating it from the
// payload if its installation event has not arrived yet. ok is false when the
// event should be ignored (suspended installation, removed or archived repo).
func (s *Service) repoForEvent(ctx context.Context, e envelope) (repo model.Repo, ok bool, err error) {
	if e.Installation.ID == 0 || e.Repository.ID == 0 {
		return repo, false, fmt.Errorf("%w: missing installation or repository", errBadPayload)
	}
	inst, err := s.st.GetInstallation(ctx, e.Installation.ID)
	if errors.Is(err, model.ErrNotFound) {
		if err := s.upsertInstallation(ctx, e.Installation); err != nil {
			return repo, false, err
		}
	} else if err != nil {
		return repo, false, err
	} else if inst.SuspendedAt != nil {
		return repo, false, nil
	}
	repo, err = s.upsertRepo(ctx, e.Installation.ID, e.Repository)
	if err != nil {
		return repo, false, err
	}
	// upsertRepo clears RemovedAt (an event proves the repo is installed).
	return repo, true, nil
}

func (s *Service) onPullRequest(ctx context.Context, e envelope) error {
	pr := e.PullRequest
	if pr.Number == 0 {
		return fmt.Errorf("%w: missing pull request", errBadPayload)
	}
	repo, ok, err := s.repoForEvent(ctx, e)
	if err != nil || !ok {
		return err
	}
	switch e.Action {
	case "opened", "reopened", "synchronize", "ready_for_review", "converted_to_draft", "edited":
		if pr.Head.SHA == "" {
			return fmt.Errorf("%w: missing head sha", errBadPayload)
		}
		_, err := s.syncPR(ctx, repo, prRef{number: pr.Number, sha: pr.Head.SHA, draft: pr.Draft}, true)
		return err
	case "closed":
		return s.prClosed(ctx, repo, pr.Number, pr.Merged)
	}
	return nil
}

func (s *Service) onCheckRun(ctx context.Context, e envelope) error {
	if e.Action != "rerequested" || e.CheckRun.Name != CheckName || e.CheckRun.HeadSHA == "" {
		return nil
	}
	repo, ok, err := s.repoForEvent(ctx, e)
	if err != nil || !ok {
		return err
	}
	sha := e.CheckRun.HeadSHA
	var errs []error
	for _, p := range e.CheckRun.PRs {
		if _, err := s.syncPR(ctx, repo, prRef{number: p.Number, sha: sha}, true); err != nil {
			errs = append(errs, err)
		}
	}
	if len(e.CheckRun.PRs) == 0 {
		// A commit with no PR (e.g. a push to the default branch).
		if err := s.checkSHA(ctx, repo, sha, 0); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// mergeQueueRef extracts the PR number from a merge group head ref such as
// "refs/heads/gh-readonly-queue/main/pr-123-<sha>".
var mergeQueueRef = regexp.MustCompile(`/pr-(\d+)-[0-9a-f]+$`)

func (s *Service) onMergeGroup(ctx context.Context, e envelope) error {
	if e.Action != "checks_requested" || e.MergeGroup.HeadSHA == "" {
		return nil
	}
	repo, ok, err := s.repoForEvent(ctx, e)
	if err != nil || !ok {
		return err
	}
	pr := 0
	if m := mergeQueueRef.FindStringSubmatch(e.MergeGroup.HeadRef); m != nil {
		pr, _ = strconv.Atoi(m[1])
	}
	return s.checkSHA(ctx, repo, e.MergeGroup.HeadSHA, pr)
}

// checkSHA posts a Check Run on a bare commit (merge group or PR-less
// rerequest). pr, if known, lets that PR's bypass apply. No PR row is stored:
// the Check Run is not tracked for reconcile.
func (s *Service) checkSHA(ctx context.Context, repo model.Repo, sha string, pr int) error {
	d, byp, err := s.evaluate(ctx, repo, pr)
	if err != nil {
		return err
	}
	_, err = s.api.CreateCheckRun(ctx, repo.InstallationID, repo.Owner, repo.Name, checkFor(sha, d, byp))
	return err
}
