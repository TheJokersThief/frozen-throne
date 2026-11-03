package slack

import (
	"strings"
	"testing"
)

func TestMaskWebhookURL(t *testing.T) {
	u := "https://hooks.slack.com/services/T0123ABCD/B0456EFGH/s3cr3tS3cr3tToken9876"
	got := MaskWebhookURL(u)
	for _, leak := range []string{"T0123ABCD", "B0456EFGH", "s3cr3t", "services"} {
		if strings.Contains(got, leak) {
			t.Fatalf("mask leaks %q: %s", leak, got)
		}
	}
	if got != "https://hooks.slack.com/…9876" {
		t.Fatalf("got %q", got)
	}
	if MaskWebhookURL("") != "" {
		t.Fatal("empty should stay empty")
	}
	if got := MaskWebhookURL("::not a url/secret-abcd"); got != "…abcd" {
		t.Fatalf("unparsable: %q", got)
	}
}

func TestValidateWebhookURL(t *testing.T) {
	ok := []struct {
		u   string
		any bool
	}{
		{"https://hooks.slack.com/services/a/b/c", false},
		{"https://hooks.slack-gov.com/services/a/b/c", false},
		{"http://127.0.0.1:1234/x", true},
		{"https://example.com/x", true},
	}
	for _, c := range ok {
		if err := ValidateWebhookURL(c.u, c.any); err != nil {
			t.Errorf("%s: %v", c.u, err)
		}
	}
	bad := []struct {
		u   string
		any bool
	}{
		{"", false},
		{"http://hooks.slack.com/services/a", false},
		{"https://example.com/x", false},
		{"https:///x", false},
		{"ftp://x/y", true},
		{"https://hooks.slack.com.evil.com/x", false},
		{"https://hooks.slack.com:8443/services/a/b/c", false},
		{"https://hooks.slack.com/other/a/b/c", false},
		{"https://hooks.slack.com/", false},
		{"https://hooks.slack.com/servicesx/a", false},
	}
	for _, c := range bad {
		if err := ValidateWebhookURL(c.u, c.any); err == nil {
			t.Errorf("%q should be invalid", c.u)
		}
	}
}

func TestMessageContent(t *testing.T) {
	base := Event{Org: "acme", Scope: "org", Actor: "alice", Reason: "release cut", DashboardURL: "https://ft.example/dash"}
	cases := []struct {
		name string
		mod  func(*Event)
		want []string
	}{
		{"freeze", func(e *Event) { e.Kind = KindFreeze }, []string{"alice", "acme", "release cut", "freeze", "<https://ft.example/dash|Open dashboard>"}},
		{"thaw", func(e *Event) { e.Kind = KindThaw }, []string{"alice", "acme", "thaw"}},
		{"bypass", func(e *Event) { e.Kind = KindBypass; e.Repo = "acme/api"; e.Scope = "repo"; e.PRNumber = 42 }, []string{"alice", "acme/api", "#42", "bypass", "release cut"}},
		{"bypass_end", func(e *Event) { e.Kind = KindBypassEnd; e.Repo = "acme/api"; e.Scope = "repo"; e.PRNumber = 42 }, []string{"acme/api", "#42", "ended"}},
		{"window_start", func(e *Event) { e.Kind = KindWindowStart; e.Actor = ""; e.Schedule = "nightly" }, []string{"schedule", "nightly", "acme"}},
		{"window_end", func(e *Event) { e.Kind = KindWindowEnd; e.Actor = ""; e.Schedule = "nightly" }, []string{"schedule", "nightly", "ended"}},
		{"window_end_overlap", func(e *Event) {
			e.Kind = KindWindowEnd
			e.Actor = ""
			e.Schedule = "nightly"
			e.StillFrozenBy = "quarter-close"
		}, []string{"nightly", "ended", "Still frozen by schedule quarter-close"}},
	}
	for _, c := range cases {
		ev := base
		c.mod(&ev)
		p := Message(ev)
		for _, w := range c.want {
			if !strings.Contains(p.Text, w) {
				t.Errorf("%s: %q missing %q", c.name, p.Text, w)
			}
		}
	}
}

func TestMessageSystemActorAndEscaping(t *testing.T) {
	p := Message(Event{Kind: KindFreeze, Org: "acme", Scope: "org", Reason: "a <!channel> & b>c"})
	if !strings.Contains(p.Text, "system") {
		t.Errorf("no system actor: %q", p.Text)
	}
	if strings.Contains(p.Text, "<!channel>") || !strings.Contains(p.Text, "&lt;!channel&gt; &amp; b&gt;c") {
		t.Errorf("not escaped: %q", p.Text)
	}
}

func TestEventAllowed(t *testing.T) {
	if !kindAllowed(nil, KindThaw) {
		t.Error("empty = all")
	}
	if !kindAllowed([]string{"bypass"}, KindBypassEnd) || kindAllowed([]string{"bypass"}, KindFreeze) {
		t.Error("bypass mapping")
	}
	if !kindAllowed([]string{"schedule"}, KindWindowStart) || !kindAllowed([]string{"schedule"}, KindWindowEnd) {
		t.Error("schedule mapping")
	}
	if !kindAllowed([]string{"freeze", "thaw"}, KindThaw) {
		t.Error("thaw")
	}
}

func TestMessageEdgeCases(t *testing.T) {
	p := Message(Event{Kind: KindBypass, Org: "acme", Scope: "org", Actor: "a"})
	if strings.Contains(p.Text, "#0") || strings.Contains(p.Text, "PR") {
		t.Errorf("zero PR shown: %q", p.Text)
	}
	p = Message(Event{Kind: KindFreeze, Org: "acme", Scope: "org", DashboardURL: "https://d/x?a=1&b=2|3"})
	if !strings.Contains(p.Text, "<https://d/x?a=1&amp;b=2%7C3|Open dashboard>") {
		t.Errorf("url not escaped: %q", p.Text)
	}
}
