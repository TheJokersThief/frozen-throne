package mockgithub_test

import (
	"context"
	"errors"
	"testing"

	"github.com/TheJokersThief/frozen-throne/internal/api"
	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/test/mockgithub"
)

func TestRepoAndOrgRoles(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	seedRepo(t, h)
	_ = h.ctl.SeedRepo(ctx, mockgithub.Repo{InstallationID: 7, ID: 12, Owner: "acme", Name: "api"})

	tokens := map[string]string{}
	for _, u := range []mockgithub.User{
		{ID: 1, Login: "owner", Token: "gho_owner", RepoPerms: map[string]string{"acme/web": "admin"}, OrgRoles: map[string]string{"acme": "admin"}},
		{ID: 2, Login: "maint", Token: "gho_maint", RepoPerms: map[string]string{"acme/web": "maintain"}, OrgRoles: map[string]string{"acme": "member"}},
		{ID: 3, Login: "dev", Token: "gho_dev", RepoPerms: map[string]string{"acme/web": "push", "acme/api": "triage"}, OrgRoles: map[string]string{"acme": "member"}},
		{ID: 4, Login: "reader", Token: "gho_reader", RepoPerms: map[string]string{"ACME/web": "pull"}},
		{ID: 5, Login: "stranger", Token: "gho_stranger"},
	} {
		if _, err := h.ctl.SeedUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		tokens[u.Login] = u.Token
	}

	g := &api.GitHubPermissions{APIBase: h.srv.URL}
	web := model.Repo{ID: 11, Owner: "acme", Name: "web"}
	apiRepo := model.Repo{ID: 12, Owner: "acme", Name: "api"}
	inst := model.Installation{ID: 7, AccountLogin: "acme", AccountType: "Organization"}

	repoWant := []struct {
		user string
		repo model.Repo
		want api.Role
	}{
		{"owner", web, api.RoleMaintainer},
		{"maint", web, api.RoleMaintainer},
		{"dev", web, api.RoleMember},
		{"dev", apiRepo, api.RoleMember},
		{"reader", web, api.RoleMember},
		{"stranger", web, api.RoleNone},
		{"maint", apiRepo, api.RoleNone},
	}
	for _, c := range repoWant {
		got, err := g.RepoRole(ctx, api.Actor{User: model.User{ID: 1}, Token: tokens[c.user]}, c.repo)
		if err != nil || got != c.want {
			t.Errorf("RepoRole(%s, %s) = %v, %v; want %v", c.user, c.repo.Name, got, err, c.want)
		}
	}

	orgWant := map[string]api.Role{"owner": api.RoleOwner, "maint": api.RoleMember, "dev": api.RoleMember, "reader": api.RoleNone, "stranger": api.RoleNone}
	for user, want := range orgWant {
		got, err := g.OrgRole(ctx, api.Actor{Token: tokens[user]}, inst)
		if err != nil || got != want {
			t.Errorf("OrgRole(%s) = %v, %v; want %v", user, got, err, want)
		}
	}

	// An unrecognised token is rejected the way GitHub does.
	_, err := g.RepoRole(ctx, api.Actor{Token: "gho_revoked"}, web)
	if !errors.Is(err, api.ErrTokenInvalid) {
		t.Fatalf("want ErrTokenInvalid, got %v", err)
	}
}

func TestRevokingPermissionsViaReseed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	seedRepo(t, h)
	g := &api.GitHubPermissions{APIBase: h.srv.URL}
	web := model.Repo{ID: 11, Owner: "acme", Name: "web"}
	u := mockgithub.User{ID: 1, Login: "x", Token: "gho_x", RepoPerms: map[string]string{"acme/web": "admin"}}
	_, _ = h.ctl.SeedUser(ctx, u)
	if r, _ := g.RepoRole(ctx, api.Actor{Token: "gho_x"}, web); r != api.RoleMaintainer {
		t.Fatalf("before: %v", r)
	}
	u.RepoPerms = nil
	_, _ = h.ctl.SeedUser(ctx, u)
	if r, _ := g.RepoRole(ctx, api.Actor{Token: "gho_x"}, web); r != api.RoleNone {
		t.Fatalf("after revoke: %v", r)
	}
}
