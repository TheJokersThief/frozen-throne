package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

var (
	// ErrNotConfigured is returned by SendTest when no webhook is stored.
	ErrNotConfigured = fmt.Errorf("slack webhook not configured: %w", model.ErrInvalid)
	// ErrQueueFull is reported to OnFailure when an event is dropped.
	ErrQueueFull = errors.New("slack notification queue full")
	// ErrClosed is reported to OnFailure for events notified after Close.
	ErrClosed = errors.New("slack notifier closed")
)

const (
	maxBackoff    = 10 * time.Second
	maxRetryAfter = 30 * time.Second
)

// closeWait bounds how long Close waits for the queue to drain (var for tests).
var closeWait = 5 * time.Second

// Options configures a Notifier.
type Options struct {
	Store       store.Slack // only GetSlackConfig is used
	// Note: the stored Channel override is ignored by modern Slack app
	// webhooks, which post to the channel chosen at install time.
	HTTPClient  *http.Client
	Timeout     time.Duration // per attempt; default 5s
	MaxAttempts int           // default 4
	BaseBackoff time.Duration // default 500ms, doubled per retry, capped
	Queue       int           // default 256
	Workers     int           // default 2
	Logger      *slog.Logger
	// OnFailure is called after a notification is dropped or finally fails.
	// The error never contains the webhook URL.
	OnFailure func(Event, error)
	// Sleep replaces the backoff wait. Test-only: an injected Sleep ignores
	// context cancellation, so Close cannot interrupt it.
	Sleep func(time.Duration)
	// AllowAnyHost permits non-Slack hosts and plain http (tests, proxies).
	AllowAnyHost bool
}

// Notifier delivers events to Slack asynchronously.
type Notifier struct {
	o      Options
	client *http.Client
	queue  chan Event

	mu     sync.RWMutex
	closed bool

	wg       sync.WaitGroup
	stopCtx  context.Context
	stop     context.CancelFunc
	closeOne sync.Once
}

// New starts a Notifier and its workers.
func New(o Options) *Notifier {
	if o.Timeout <= 0 {
		o.Timeout = 5 * time.Second
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 4
	}
	if o.BaseBackoff <= 0 {
		o.BaseBackoff = 500 * time.Millisecond
	}
	if o.Queue <= 0 {
		o.Queue = 256
	}
	if o.Workers <= 0 {
		o.Workers = 2
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	n := &Notifier{o: o, queue: make(chan Event, o.Queue)}
	n.client = o.HTTPClient
	if n.client == nil {
		n.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	n.stopCtx, n.stop = context.WithCancel(context.Background())
	for i := 0; i < o.Workers; i++ {
		n.wg.Add(1)
		go n.worker()
	}
	return n
}

// Notify enqueues ev for delivery. It never blocks: if the queue is full (or
// the notifier is closed) the event is dropped, logged and reported to
// OnFailure.
func (n *Notifier) Notify(ev Event) {
	// Hold the lock only for the send; callbacks and logging run unlocked so
	// a slow or re-entrant OnFailure cannot stall Close or other callers.
	var dropErr error
	n.mu.RLock()
	if n.closed {
		dropErr = ErrClosed
	} else {
		select {
		case n.queue <- ev:
		default:
			dropErr = ErrQueueFull
		}
	}
	n.mu.RUnlock()
	if dropErr == nil {
		return
	}
	if dropErr == ErrQueueFull {
		n.o.Logger.Warn("slack notification dropped: queue full", "kind", ev.Kind, "org", ev.Org)
	}
	n.fail(ev, dropErr)
}

// Close stops accepting events, lets workers drain the queue for a bounded
// time, then cancels in-flight sends. Safe to call more than once.
func (n *Notifier) Close() {
	n.closeOne.Do(func() {
		n.mu.Lock()
		n.closed = true
		close(n.queue)
		n.mu.Unlock()
		done := make(chan struct{})
		go func() { n.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(closeWait):
			n.stop()
			<-done
		}
		n.stop()
	})
}

func (n *Notifier) fail(ev Event, err error) {
	if n.o.OnFailure != nil {
		n.o.OnFailure(ev, err)
	}
}

func (n *Notifier) worker() {
	defer n.wg.Done()
	for ev := range n.queue {
		n.handle(ev)
	}
}

func (n *Notifier) handle(ev Event) {
	ctx, cancel := context.WithTimeout(n.stopCtx, n.o.Timeout)
	cfg, err := n.o.Store.GetSlackConfig(ctx, ev.InstallationID)
	cancel()
	if errors.Is(err, model.ErrNotFound) {
		return
	}
	if err != nil {
		n.o.Logger.Warn("slack config load failed", "installation", ev.InstallationID, "err", err)
		n.fail(ev, fmt.Errorf("load slack config: %w", err))
		return
	}
	if !cfg.Enabled || cfg.WebhookURL == "" || !kindAllowed(cfg.Events, ev.Kind) {
		return
	}
	p := Message(ev)
	p.Channel = cfg.Channel
	if err := n.deliver(n.stopCtx, cfg.WebhookURL, p); err != nil {
		n.o.Logger.Warn("slack notification failed", "kind", ev.Kind, "org", ev.Org,
			"webhook", MaskWebhookURL(cfg.WebhookURL), "err", err)
		n.fail(ev, err)
	}
}

// SendTest synchronously posts a test message using the stored webhook,
// ignoring the Enabled flag and event filter. No retries, no queue.
func (n *Notifier) SendTest(ctx context.Context, installationID int64, org, dashboardURL string) error {
	cfg, err := n.o.Store.GetSlackConfig(ctx, installationID)
	if errors.Is(err, model.ErrNotFound) {
		return ErrNotConfigured
	}
	if err != nil {
		return fmt.Errorf("load slack config: %w", err)
	}
	if cfg.WebhookURL == "" {
		return ErrNotConfigured
	}
	text := fmt.Sprintf(":white_check_mark: Frozen Throne test notification for *%s*", esc(org))
	if dashboardURL != "" {
		text += "\n<" + linkEscaper.Replace(dashboardURL) + "|Open dashboard>"
	}
	return n.attempt(ctx, cfg.WebhookURL, Payload{Text: text, Channel: cfg.Channel})
}

// statusError is a non-2xx Slack response.
type statusError struct {
	code       int
	retryAfter time.Duration
}

func (e *statusError) Error() string { return fmt.Sprintf("slack returned HTTP %d", e.code) }

func (e *statusError) retryable() bool { return e.code == 429 || e.code >= 500 }

// deliver posts p, retrying transient failures with exponential backoff.
func (n *Notifier) deliver(ctx context.Context, webhook string, p Payload) error {
	var err error
	for attempt := 1; attempt <= n.o.MaxAttempts; attempt++ {
		err = n.attempt(ctx, webhook, p)
		if err == nil {
			return nil
		}
		se, isStatus := errors.AsType[*statusError](err)
		if isStatus && !se.retryable() {
			return err
		}
		if errors.Is(err, errInvalidURL) || ctx.Err() != nil || attempt == n.o.MaxAttempts {
			break
		}
		wait := n.o.BaseBackoff << (attempt - 1)
		if wait > maxBackoff || wait <= 0 {
			wait = maxBackoff
		}
		if isStatus && se.code == 429 && se.retryAfter > 0 && se.retryAfter <= maxRetryAfter {
			wait = se.retryAfter
		}
		n.sleep(ctx, wait)
	}
	return err
}

func (n *Notifier) sleep(ctx context.Context, d time.Duration) {
	if n.o.Sleep != nil {
		n.o.Sleep(d)
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

var (
	errInvalidURL = errors.New("invalid webhook URL")
)

// attempt makes one POST with the per-attempt timeout. Returned errors never
// include the webhook URL.
func (n *Notifier) attempt(ctx context.Context, webhook string, p Payload) error {
	if err := ValidateWebhookURL(webhook, n.o.AllowAnyHost); err != nil {
		return fmt.Errorf("%w: %v", errInvalidURL, err)
	}
	body, err := json.Marshal(p)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, n.o.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(body))
	if err != nil {
		return errInvalidURL
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.client.Do(req)
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err // net/http embeds the full URL in url.Error
		}
		return fmt.Errorf("slack request failed: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 == 2 {
		return nil
	}
	se := &statusError{code: resp.StatusCode}
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		se.retryAfter = time.Duration(s) * time.Second
	}
	return se
}
