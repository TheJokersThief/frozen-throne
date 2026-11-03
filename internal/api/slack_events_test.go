package api

import (
	"context"
	"strings"
	"testing"
	"time"
)

func containsAll(t *testing.T, text string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(text, p) {
			t.Fatalf("message %q missing %q", text, p)
		}
	}
}

func slackEnv(t *testing.T, events ...string) (*env, *fakeSlack) {
	t.Helper()
	f := newFakeSlack(t)
	e := newEnv(t, withSlack(f))
	e.putSlack(f.url(), true, events...)
	return e, f
}

func TestSlackRepoFreezeAndThaw(t *testing.T) {
	e, f := slackEnv(t)
	bob := e.login("bob")
	e.expect(e.do(bob, "POST", "/repos/acme/web/freeze", map[string]string{"reason": "release cut"}), 201)
	containsAll(t, f.wait(t).Text, "bob", "started a freeze", "repo *acme/web*", "release cut", "<https://ft.example/repos/acme/web|")
	f.none(t)

	e.expect(e.do(bob, "DELETE", "/repos/acme/web/freeze", nil), 200)
	containsAll(t, f.wait(t).Text, "bob", "thawed", "repo *acme/web*", "https://ft.example/repos/acme/web")
	f.none(t)
}

func TestSlackOrgFreezeAndThaw(t *testing.T) {
	e, f := slackEnv(t)
	alice := e.login("alice")
	e.expect(e.do(alice, "POST", "/orgs/acme/freeze", map[string]string{"reason": "holiday"}), 201)
	containsAll(t, f.wait(t).Text, "alice", "started a freeze", "org *acme*", "holiday", "https://ft.example/orgs/acme")

	e.expect(e.do(alice, "DELETE", "/orgs/acme/freeze", nil), 200)
	containsAll(t, f.wait(t).Text, "alice", "thawed", "org *acme*", "https://ft.example/orgs/acme")
}

func TestSlackBypassCreateAndEnd(t *testing.T) {
	e, f := slackEnv(t)
	bob := e.login("bob")
	e.expect(e.do(bob, "POST", "/repos/acme/web/freeze", map[string]string{"reason": "r"}), 201)
	f.wait(t)
	e.expect(e.do(bob, "POST", "/repos/acme/web/pulls/42/bypass", map[string]string{"reason": "hotfix"}), 201)
	containsAll(t, f.wait(t).Text, "bob", "granted a bypass", "PR #42", "repo *acme/web*", "hotfix", "https://ft.example/repos/acme/web")

	e.expect(e.do(bob, "DELETE", "/repos/acme/web/pulls/42/bypass", nil), 204)
	containsAll(t, f.wait(t).Text, "bypass for PR #42", "ended", "acme/web")
	f.none(t)
}

func TestSlackEventFilterAndDisabled(t *testing.T) {
	e, f := slackEnv(t, "thaw")
	bob := e.login("bob")
	e.expect(e.do(bob, "POST", "/repos/acme/web/freeze", `{}`), 201)
	f.none(t) // freeze filtered out

	e.putSlack("", false) // disable, keep URL
	e.expect(e.do(bob, "DELETE", "/repos/acme/web/freeze", nil), 200)
	f.none(t)
}

func TestSlackFailureDoesNotAffectFreeze(t *testing.T) {
	for name, setup := range map[string]func(*fakeSlack){
		"500":  func(f *fakeSlack) { f.status.Store(500) },
		"hang": func(f *fakeSlack) { f.hang.Store(true) },
	} {
		t.Run(name, func(t *testing.T) {
			e, f := slackEnv(t)
			setup(f)
			defer f.release() // let hanging handlers finish before cleanup waits on them
			start := time.Now()
			e.expect(e.do(e.login("bob"), "POST", "/repos/acme/web/freeze", map[string]string{"reason": "x"}), 201)
			if d := time.Since(start); d > 400*time.Millisecond {
				t.Fatalf("freeze took %v", d)
			}
			if _, err := e.st.ActiveRepoFreeze(context.Background(), e.web.ID); err != nil {
				t.Fatalf("freeze not persisted: %v", err)
			}
		})
	}
}

func TestSlackNotConfiguredIsSilent(t *testing.T) {
	f := newFakeSlack(t)
	e := newEnv(t, withSlack(f))
	e.expect(e.do(e.login("bob"), "POST", "/repos/acme/web/freeze", `{}`), 201)
	f.none(t)
}
