package mockgithub_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestFakeSlackRecordsPostsAndResets(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	resp, err := http.Post(h.srv.URL+"/_mock/slack/hooks/T1", "application/json", strings.NewReader(`{"text":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("slack post status = %d", resp.StatusCode)
	}

	posts, err := h.ctl.SlackPosts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || posts[0].Hook != "T1" || !strings.Contains(posts[0].Body, "hello") {
		t.Fatalf("posts = %+v", posts)
	}

	if err := h.ctl.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if posts, _ = h.ctl.SlackPosts(ctx); len(posts) != 0 {
		t.Fatalf("reset kept posts: %+v", posts)
	}
}
