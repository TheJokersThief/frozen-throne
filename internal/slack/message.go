// Package slack delivers freeze/thaw/bypass/schedule notifications to a Slack
// incoming webhook. Delivery is asynchronous and best-effort: callers enqueue
// events with Notifier.Notify, which never blocks.
package slack

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Kind identifies what happened.
type Kind string

const (
	KindFreeze      Kind = "freeze"
	KindThaw        Kind = "thaw"
	KindBypass      Kind = "bypass"
	KindBypassEnd   Kind = "bypass_end"
	KindWindowStart Kind = "window_start"
	KindWindowEnd   Kind = "window_end"
)

// Event is one notifiable occurrence.
type Event struct {
	InstallationID int64
	Kind           Kind
	Org            string
	Repo           string // owner/name; empty for org-wide
	Scope          string // "org" or "repo"
	Actor          string // empty = system
	Reason         string
	PRNumber       int
	Schedule       string // schedule name
	DashboardURL   string
}

// Payload is the JSON body posted to a Slack incoming webhook.
type Payload struct {
	Text    string `json:"text"`
	Channel string `json:"channel,omitempty"`
}

// kindAllowed reports whether the configured event filter (freeze|thaw|bypass|
// schedule; empty = all) lets kind through.
func kindAllowed(events []string, k Kind) bool {
	if len(events) == 0 {
		return true
	}
	for _, e := range events {
		switch e {
		case "freeze":
			if k == KindFreeze {
				return true
			}
		case "thaw":
			if k == KindThaw {
				return true
			}
		case "bypass":
			if k == KindBypass || k == KindBypassEnd {
				return true
			}
		case "schedule":
			if k == KindWindowStart || k == KindWindowEnd {
				return true
			}
		}
	}
	return false
}

var slackEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// esc escapes user-supplied text per Slack's mrkdwn rules.
func esc(s string) string { return slackEscaper.Replace(s) }

// Message builds the webhook payload for ev.
func Message(ev Event) Payload {
	target := ev.Org
	if ev.Repo != "" {
		target = ev.Repo
	}
	if ev.Scope == "" {
		ev.Scope = "org"
	}
	target = fmt.Sprintf("%s *%s*", ev.Scope, esc(target))

	actor := ev.Actor
	if actor == "" {
		if ev.Schedule != "" {
			actor = "schedule " + ev.Schedule
		} else {
			actor = "system"
		}
	}
	actor = esc(actor)
	sched := ""
	if ev.Schedule != "" {
		sched = fmt.Sprintf(" (schedule %s)", esc(ev.Schedule))
	}

	var head string
	switch ev.Kind {
	case KindFreeze:
		head = fmt.Sprintf(":snowflake: %s started a freeze on %s", actor, target)
	case KindThaw:
		head = fmt.Sprintf(":sunny: %s thawed %s", actor, target)
	case KindBypass:
		head = fmt.Sprintf(":unlock: %s granted a bypass for PR #%d on %s", actor, ev.PRNumber, target)
	case KindBypassEnd:
		head = fmt.Sprintf(":lock: bypass for PR #%d on %s ended", ev.PRNumber, target)
	case KindWindowStart:
		head = fmt.Sprintf(":snowflake: freeze window started by schedule on %s%s", target, sched)
	case KindWindowEnd:
		head = fmt.Sprintf(":sunny: freeze window ended by schedule on %s%s", target, sched)
	default:
		head = fmt.Sprintf("%s: %s on %s", esc(string(ev.Kind)), actor, target)
	}

	var b strings.Builder
	b.WriteString(head)
	if ev.Reason != "" {
		b.WriteString("\nReason: " + esc(ev.Reason))
	}
	if ev.DashboardURL != "" {
		b.WriteString("\n<" + strings.NewReplacer("|", "%7C", ">", "%3E", "<", "%3C").Replace(ev.DashboardURL) + "|Open dashboard>")
	}
	return Payload{Text: b.String()}
}

// MaskWebhookURL returns a log-safe form of a webhook URL. Slack webhook URLs
// carry their secret in the path, so only the scheme and host plus the last
// four characters are kept: "https://hooks.slack.com/…abcd". An empty input
// gives ""; an unparsable one gives "…" plus its last four characters.
func MaskWebhookURL(u string) string {
	if u == "" {
		return ""
	}
	tail := ""
	if len(u) >= 12 {
		tail = u[len(u)-4:]
	}
	p, err := url.Parse(u)
	if err != nil || p.Scheme == "" || p.Host == "" {
		return "…" + tail
	}
	return p.Scheme + "://" + p.Host + "/…" + tail
}

// ValidateWebhookURL checks u is usable as a webhook target. It must be https
// with a host, and unless allowAnyHost the host must be hooks.slack.com or
// hooks.slack-gov.com (limits SSRF). allowAnyHost also permits plain http.
// Errors never contain the URL.
func ValidateWebhookURL(u string, allowAnyHost bool) error {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" || p.Hostname() == "" {
		return errors.New("webhook URL is not a valid absolute URL")
	}
	if allowAnyHost {
		if p.Scheme != "https" && p.Scheme != "http" {
			return errors.New("webhook URL must use https")
		}
		return nil
	}
	if p.Scheme != "https" {
		return errors.New("webhook URL must use https")
	}
	switch strings.ToLower(p.Hostname()) {
	case "hooks.slack.com", "hooks.slack-gov.com":
		return nil
	}
	return errors.New("webhook host must be hooks.slack.com or hooks.slack-gov.com")
}
