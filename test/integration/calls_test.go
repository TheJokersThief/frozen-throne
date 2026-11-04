//go:build integration

package integration

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAppAuthenticatesToGitHubCorrectly(t *testing.T) {
	e := stack.Reset(t)
	e.Install()
	sha := e.SHA("head")
	e.OpenPR(2, sha)
	e.WaitCheck(sha, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	calls, err := e.S.Mock.Calls(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var tokenSeq, checkSeq int
	for _, c := range calls {
		switch {
		case c.Method == "POST" && strings.HasSuffix(c.Path, "/access_tokens"):
			// The mock verifies the RS256 signature, iss and expiry before
			// answering 201, so a 201 means the JWT was valid.
			if c.Status != 201 {
				t.Errorf("token request %+v: want 201", c)
			}
			if !strings.Contains(c.Path, "/installations/"+itoa(e.InstallationID)+"/") {
				t.Errorf("token requested for the wrong installation: %q", c.Path)
			}
			if got := c.Headers["Authorization"]; !strings.HasSuffix(got, "<app-jwt>") {
				t.Errorf("token request authorization = %q, want a Bearer app JWT", got)
			}
			if tokenSeq == 0 {
				tokenSeq = c.Seq
			}
		case c.Method == "POST" && strings.HasSuffix(c.Path, "/check-runs"):
			if c.Status != 201 {
				t.Errorf("check-run POST %+v: want 201", c)
			}
			if got := c.Headers["Authorization"]; !strings.HasSuffix(got, "<installation-token>") {
				t.Errorf("check-run authorization = %q, want an installation token", got)
			}
			if !strings.Contains(c.Body, sha) || !strings.Contains(c.Body, CheckName) {
				t.Errorf("check-run body lacks sha/name: %s", c.Body)
			}
			if checkSeq == 0 {
				checkSeq = c.Seq
			}
		}
	}
	if tokenSeq == 0 || checkSeq == 0 {
		t.Fatalf("missing calls: token seq %d, check-run seq %d in %+v", tokenSeq, checkSeq, calls)
	}
	if tokenSeq > checkSeq {
		t.Fatalf("check run (#%d) was posted before the token was obtained (#%d)", checkSeq, tokenSeq)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
