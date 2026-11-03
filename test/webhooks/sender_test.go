package webhooks_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	ghsvc "github.com/TheJokersThief/frozen-throne/internal/github"
	"github.com/TheJokersThief/frozen-throne/test/webhooks"
)

type captured struct {
	h    http.Header
	body []byte
	path string
}

func capture(t *testing.T, status int) (*httptest.Server, chan captured) {
	ch := make(chan captured, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- captured{r.Header.Clone(), b, r.URL.Path}
		w.WriteHeader(status)
		w.Write([]byte("hi"))
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

func TestSenderHeadersAndSignature(t *testing.T) {
	srv, ch := capture(t, 202)
	s := webhooks.NewSender(srv.URL+"/webhooks/github", []byte("s3cret"),
		webhooks.WithHookID(42), webhooks.WithTargetID(1234))
	d := webhooks.PullRequestOpened(webhooks.Params{PRNumber: 1, HeadSHA: sha, InstallationID: 77})
	resp, err := s.Send(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	c := <-ch
	if c.path != "/webhooks/github" {
		t.Errorf("path %q", c.path)
	}
	want := map[string]string{
		"X-Github-Event":                         "pull_request",
		"Content-Type":                           "application/json",
		"X-Github-Hook-Id":                       "42",
		"X-Github-Hook-Installation-Target-Type": "integration",
		"X-Github-Hook-Installation-Target-Id":   "1234",
	}
	for k, v := range want {
		if got := c.h.Get(k); got != v {
			t.Errorf("%s = %q want %q", k, got, v)
		}
	}
	if ua := c.h.Get("User-Agent"); !regexp.MustCompile(`^GitHub-Hookshot/`).MatchString(ua) {
		t.Errorf("UA %q", ua)
	}
	id := c.h.Get("X-Github-Delivery")
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(id) {
		t.Errorf("delivery id %q", id)
	}
	if resp.DeliveryID != id || resp.Status != 202 || string(resp.Body) != "hi" {
		t.Errorf("resp %+v", resp)
	}
	if !ghsvc.VerifySignature([]byte("s3cret"), c.body, c.h.Get("X-Hub-Signature-256")) {
		t.Error("sha256 signature does not verify")
	}
	if got := c.h.Get("X-Hub-Signature"); len(got) != len("sha1=")+40 || got[:5] != "sha1=" {
		t.Errorf("sha1 header %q", got)
	}
	if string(c.body) != string(d.Body) {
		t.Error("body altered")
	}
}

func TestSenderDeliveryIDsUnique(t *testing.T) {
	srv, ch := capture(t, 204)
	s := webhooks.NewSender(srv.URL, []byte("x"))
	a, _ := s.Send(context.Background(), webhooks.Ping(webhooks.Params{}))
	b, _ := s.Send(context.Background(), webhooks.Ping(webhooks.Params{}))
	<-ch
	<-ch
	if a.DeliveryID == b.DeliveryID {
		t.Fatal("delivery ids repeat")
	}
}

func TestSenderSignatureOptions(t *testing.T) {
	d := webhooks.Ping(webhooks.Params{})
	srv, ch := capture(t, 204)

	s := webhooks.NewSender(srv.URL, []byte("k"), webhooks.WithoutSignature())
	s.Send(context.Background(), d)
	c := <-ch
	if c.h.Get("X-Hub-Signature-256") != "" || c.h.Get("X-Hub-Signature") != "" {
		t.Error("signature headers present")
	}

	s = webhooks.NewSender(srv.URL, []byte("k"), webhooks.WithInvalidSignature())
	s.Send(context.Background(), d)
	c = <-ch
	if c.h.Get("X-Hub-Signature-256") == "" || ghsvc.VerifySignature([]byte("k"), c.body, c.h.Get("X-Hub-Signature-256")) {
		t.Error("expected present but invalid signature")
	}
}
