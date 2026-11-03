// Package app wires the real frozen-throne packages (SQLite store, engine,
// GitHub client and webhook service, HTTP API) into one http.Handler for the
// integration harness, pointed at a mock GitHub.
//
// This is a stand-in: cmd/frozen-throne is still a scaffold owned elsewhere.
// Once it grows real wiring, the harness should run that binary instead and
// this package can go away. Until then it is the extension point where the
// reconcile loop (see Notifier below) would be added.
package app
