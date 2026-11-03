// Package github is the GitHub App integration: a webhook receiver with HMAC
// signature verification, App JWT and per-installation token handling, the
// "frozen-throne" Check Run, and installation/repo sync into the store.
//
// The package depends only on the store interfaces and the engine.Evaluator
// (injected), and talks to GitHub through the small API interface, which the
// concrete Client implements and tests replace with an httptest server.
package github
