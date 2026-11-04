// Command dev-server runs the real HTTP API (internal/api) against a local
// SQLite file with a fake GitHub login, so the web dashboard can be developed
// without GitHub credentials or the (not yet wired) production binary.
//
// DEV ONLY. Anyone who can reach the listener is logged in as "octocat" with
// the configured role. Never deploy or expose it.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/api"
	"github.com/TheJokersThief/frozen-throne/internal/engine"
	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

// devUser is the only identity the fake login ever produces.
var devUser = model.User{ID: 1, Login: "octocat", Name: "Octo Cat"}

// devOAuth is an OAuthProvider that needs no GitHub. AuthorizeURL is a
// relative URL so the browser stays on the origin it started from (the vite
// dev server proxying /api) and the round trip is same-origin.
type devOAuth struct{}

func (devOAuth) AuthorizeURL(state string) string {
	return api.Prefix + "/auth/callback?" + url.Values{"code": {"good"}, "state": {state}}.Encode()
}

func (devOAuth) Exchange(_ context.Context, code string) (string, error) {
	if code != "good" {
		return "", api.ErrBadCode
	}
	return "dev-token", nil
}

func (devOAuth) User(context.Context, string) (model.User, error) { return devUser, nil }

// devPerms grants every user the same configured role.
type devPerms struct{ repo, org api.Role }

func newDevPerms(role string) (devPerms, error) {
	switch role {
	case "admin":
		return devPerms{repo: api.RoleMaintainer, org: api.RoleOwner}, nil
	case "write": // may freeze repos, not orgs
		return devPerms{repo: api.RoleMaintainer, org: api.RoleMember}, nil
	case "read":
		return devPerms{repo: api.RoleMember, org: api.RoleMember}, nil
	case "none":
		return devPerms{}, nil
	}
	return devPerms{}, fmt.Errorf("unknown role %q (want admin|write|read|none)", role)
}

func (p devPerms) RepoRole(context.Context, api.Actor, model.Repo) (api.Role, error) {
	return p.repo, nil
}

func (p devPerms) OrgRole(context.Context, api.Actor, model.Installation) (api.Role, error) {
	return p.org, nil
}

// seed fills an empty store with the same orgs and repos as the web mock.
func seed(ctx context.Context, st store.Store) error {
	if existing, err := st.ListInstallations(ctx); err != nil {
		return err
	} else if len(existing) > 0 {
		return nil
	}
	insts := []model.Installation{
		{ID: 1, AccountLogin: "acme", AccountID: 100, AccountType: "Organization"},
		{ID: 2, AccountLogin: "globex", AccountID: 200, AccountType: "Organization"},
		{ID: 3, AccountLogin: "octocat", AccountID: devUser.ID, AccountType: "User"},
	}
	for _, i := range insts {
		if err := st.UpsertInstallation(ctx, i); err != nil {
			return err
		}
	}
	id := int64(100)
	add := func(inst int64, owner, name string, private, archived, public bool) error {
		id++
		return st.UpsertRepo(ctx, model.Repo{
			ID: id, InstallationID: inst, Owner: owner, Name: name, DefaultBranch: "main",
			Private: private, Archived: archived, PublicStatus: public,
		})
	}
	type r struct {
		inst                      int64
		owner, name               string
		private, archived, public bool
	}
	repos := []r{
		{1, "acme", "web", false, false, false}, {1, "acme", "api", true, false, false},
		{1, "acme", "mobile", false, false, false}, {1, "acme", "infra", true, false, false},
		{1, "acme", "docs", false, false, true}, {1, "acme", "legacy-billing", false, true, false},
		{2, "globex", "reactor", false, false, false}, {2, "globex", "dashboard", false, false, false},
		{3, "octocat", "dotfiles", false, false, false}, {3, "octocat", "hello-world", false, false, false},
	}
	for _, n := range []string{"search", "billing", "auth", "gateway", "emails", "analytics", "ml-pipeline", "ci-tools", "design-system", "cli"} {
		repos = append(repos, r{1, "acme", n, false, false, false})
	}
	for _, x := range repos {
		if err := add(x.inst, x.owner, x.name, x.private, x.archived, x.public); err != nil {
			return err
		}
	}
	return nil
}

// newServer assembles the API; split from main for tests.
func newServer(ctx context.Context, st store.Store, role string, secret []byte) (*api.Server, error) {
	perms, err := newDevPerms(role)
	if err != nil {
		return nil, err
	}
	if err := seed(ctx, st); err != nil {
		return nil, fmt.Errorf("seed: %w", err)
	}
	return api.New(api.Config{
		SessionSecret: secret, SecureCookies: false, PermissionCacheTTL: -1,
	}, api.Deps{Store: st, Engine: engine.New(), OAuth: devOAuth{}, Permissions: perms})
}

// checkBindAddr refuses non-loopback listen addresses (including an empty
// host, which binds every interface) unless allowRemote is set: the dev server
// logs everyone in, so it must not be reachable from the network by accident.
func checkBindAddr(addr string, allowRemote bool) error {
	if allowRemote {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid -addr %q: %w", addr, err)
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("refusing to listen on non-loopback address %q: the dev server logs everyone in; pass -allow-remote to override", addr)
}

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address (must be loopback unless -allow-remote)")
	allowRemote := flag.Bool("allow-remote", false, "allow a non-loopback -addr (anyone who can reach it is logged in)")
	dbPath := flag.String("db", "./dev.db", "SQLite database file (seeded when empty)")
	role := flag.String("role", "admin", "role of the dev user: admin|write|read|none")
	flag.Parse()
	if err := checkBindAddr(*addr, *allowRemote); err != nil {
		log.Fatal(err)
	}

	secret := []byte(os.Getenv("FT_DEV_SESSION_SECRET"))
	if len(secret) == 0 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			log.Fatal(err)
		}
	} else if len(secret) < 32 {
		secret = bytes.Repeat(secret, 32/len(secret)+1)
	}

	ctx := context.Background()
	st, err := store.Open(ctx, *dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer st.Close()
	srv, err := newServer(ctx, st, *role, secret)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("dev-server (DEV ONLY, no real auth) on %s, db %s, role %s", *addr, *dbPath, *role)
	hs := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
