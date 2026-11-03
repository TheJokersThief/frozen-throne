package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // GitHub still sends X-Hub-Signature (SHA-1).
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// Response is what the receiver answered.
type Response struct {
	Status     int
	Body       []byte
	DeliveryID string
}

// Sender delivers signed webhooks to a URL.
type Sender struct {
	url, hookID, targetID string
	secret                []byte
	client                *http.Client
	noSig, badSig         bool
}

// Option configures a Sender.
type Option func(*Sender)

// WithHTTPClient sets the HTTP client.
func WithHTTPClient(c *http.Client) Option { return func(s *Sender) { s.client = c } }

// WithHookID sets X-GitHub-Hook-ID.
func WithHookID(id int64) Option { return func(s *Sender) { s.hookID = strconv.FormatInt(id, 10) } }

// WithTargetID sets X-GitHub-Hook-Installation-Target-ID (the App id).
func WithTargetID(id int64) Option { return func(s *Sender) { s.targetID = strconv.FormatInt(id, 10) } }

// WithoutSignature omits both signature headers.
func WithoutSignature() Option { return func(s *Sender) { s.noSig = true } }

// WithInvalidSignature sends well-formed signature headers that do not match
// the body.
func WithInvalidSignature() Option { return func(s *Sender) { s.badSig = true } }

// NewSender returns a Sender posting to url, signing with secret.
func NewSender(url string, secret []byte, opts ...Option) *Sender {
	s := &Sender{url: url, secret: secret, client: http.DefaultClient, hookID: "987654321", targetID: "123456"}
	for _, o := range opts {
		o(s)
	}
	return s
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Send posts d and returns the receiver's response. A non-2xx status is not an
// error.
func (s *Sender) Send(ctx context.Context, d Delivery) (Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(d.Body))
	if err != nil {
		return Response{}, err
	}
	id := newUUID()
	h := req.Header
	h.Set("Content-Type", "application/json")
	h.Set("User-Agent", "GitHub-Hookshot/frozen-throne-test")
	h.Set("Accept", "*/*")
	h.Set("X-GitHub-Event", d.Event)
	h.Set("X-GitHub-Delivery", id)
	h.Set("X-GitHub-Hook-ID", s.hookID)
	h.Set("X-GitHub-Hook-Installation-Target-Type", "integration")
	h.Set("X-GitHub-Hook-Installation-Target-ID", s.targetID)
	if !s.noSig {
		body := d.Body
		if s.badSig {
			body = append([]byte("tampered:"), body...)
		}
		m256 := hmac.New(sha256.New, s.secret)
		m256.Write(body)
		h.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(m256.Sum(nil)))
		m1 := hmac.New(sha1.New, s.secret)
		m1.Write(body)
		h.Set("X-Hub-Signature", "sha1="+hex.EncodeToString(m1.Sum(nil)))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return Response{}, err
	}
	return Response{Status: resp.StatusCode, Body: b, DeliveryID: id}, nil
}
