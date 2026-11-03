//go:build integration

package integration

import "testing"

// Repo ids from AddRepo must never equal the RepoID of the next env, which the
// app's database would see as the same repository.
func TestRepoIDsNeverCollideAcrossEnvs(t *testing.T) {
	seen := map[int64]string{}
	check := func(id int64, what string) {
		t.Helper()
		if prev, ok := seen[id]; ok {
			t.Fatalf("repo id %d used by both %s and %s", id, prev, what)
		}
		seen[id] = what
	}
	for i := 0; i < 3; i++ {
		e := stack.Reset(t)
		check(e.RepoID, "env repo")
		check(e.AddRepo("extra1"), "added repo 1")
		check(e.AddRepo("extra2"), "added repo 2")
	}
}
