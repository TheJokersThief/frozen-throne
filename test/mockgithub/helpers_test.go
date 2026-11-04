package mockgithub_test

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/github"
	"github.com/TheJokersThief/frozen-throne/test/mockgithub"
)

const (
	appID        = 4242
	clientID     = "Iv1.testclient"
	clientSecret = "test-secret"
)

func loadKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	b, err := os.ReadFile("../testdata/app-key.pem")
	if err != nil {
		t.Fatal(err)
	}
	k, err := github.ParsePrivateKey(b)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// harness is a running mock plus its control client.
type harness struct {
	srv *httptest.Server
	ctl *mockgithub.Client
	key *rsa.PrivateKey
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	key := loadKey(t)
	gh, err := mockgithub.New(mockgithub.Config{
		AppID:        appID,
		AppPublicKey: &key.PublicKey,
		ClientID:     clientID,
		ClientSecret: clientSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)
	return &harness{srv: srv, ctl: mockgithub.NewClient(srv.URL), key: key}
}

// app returns the real production GitHub client pointed at the mock.
func (h *harness) app(t *testing.T) *github.Client {
	t.Helper()
	c, err := github.NewClient(github.Config{AppID: appID, PrivateKey: h.key, BaseURL: h.srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// do performs a raw request and returns status and body.
func do(t *testing.T, method, url, bearer string, body any) (int, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data
}

// signJWT builds an App JWT the way GitHub documents it.
func signJWT(t *testing.T, key *rsa.PrivateKey, iss int) string {
	t.Helper()
	enc := base64.RawURLEncoding
	now := time.Now()
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": strconv.Itoa(iss)})
	signing := enc.EncodeToString(hdr) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + enc.EncodeToString(sig)
}
