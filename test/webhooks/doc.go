// Package webhooks provides realistic GitHub App webhook payloads and a Sender
// that delivers them to a URL signed and headed the way GitHub's hookshot does.
//
// Fixtures are text/template JSON files under fixtures/. Use the typed
// constructors (PullRequestOpened, ...) or Render("pull_request.opened", p).
package webhooks
