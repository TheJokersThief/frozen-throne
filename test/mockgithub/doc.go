// Package mockgithub is an in-memory fake of the parts of GitHub that
// frozen-throne talks to: the App installation-token flow, pull requests,
// check runs, repository and organization permissions, and the OAuth web flow.
//
// New returns an http.Handler. GitHub's REST surface is served at the paths
// GitHub uses; a JSON control API under /_mock/ seeds state and inspects what
// the application did. Client wraps the control API for Go tests.
package mockgithub
