package slack

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/engine"
	"github.com/TheJokersThief/frozen-throne/internal/model"
	"github.com/TheJokersThief/frozen-throne/internal/store"
)

// EventNotifier receives events; *Notifier implements it.
type EventNotifier interface{ Notify(Event) }

// WindowOptions configures a WindowWatcher.
type WindowOptions struct {
	Store        store.Store
	Notifier     EventNotifier
	Interval     time.Duration    // default 30s
	Now          func() time.Time // default time.Now
	DashboardURL string           // base URL for links; empty omits them
	Logger       *slog.Logger
}

// winState is what the watcher remembers about one schedule.
type winState struct {
	active bool
	sched  model.Schedule // last seen definition (used when it is deleted)
}

// WindowWatcher notifies when a schedule window starts or ends. Nothing in
// the system fires on those instants, so it polls: each Tick it computes
// which enabled schedules are inside a window and diffs that against the
// previous tick. State is in memory; the first tick only records it, so a
// restart never re-announces windows that are already open (and never
// announces a window that ended while the process was down).
type WindowWatcher struct {
	o WindowOptions

	mu     sync.Mutex
	seeded bool
	state  map[int64]*winState
	logins map[int64]string // installation id -> account login
	repos  map[int64]string // repo id -> owner/name (last known)
	fresh  map[int64]bool   // repo ids re-read this tick
}

// NewWindowWatcher builds a WindowWatcher.
func NewWindowWatcher(o WindowOptions) *WindowWatcher {
	if o.Interval <= 0 {
		o.Interval = 30 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &WindowWatcher{
		o: o, state: map[int64]*winState{}, logins: map[int64]string{}, repos: map[int64]string{},
	}
}

// Run ticks until ctx is cancelled.
func (w *WindowWatcher) Run(ctx context.Context) {
	w.Tick(ctx, w.o.Now())
	t := time.NewTicker(w.o.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Tick(ctx, w.o.Now())
		}
	}
}

// Tick evaluates every enabled schedule at now and emits start/end events for
// the ones that changed since the previous tick.
func (w *WindowWatcher) Tick(ctx context.Context, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()

	insts, err := w.o.Store.ListInstallations(ctx)
	if err != nil {
		w.o.Logger.Warn("window watcher: list installations failed", "err", err)
		return
	}
	current := map[int64]model.Schedule{}
	for _, inst := range insts {
		scheds, err := w.o.Store.ListSchedules(ctx, store.ScheduleFilter{InstallationID: inst.ID, EnabledOnly: true})
		if err != nil {
			// Abort without touching state: a partial view would look
			// like schedules ended.
			w.o.Logger.Warn("window watcher: list schedules failed", "installation", inst.ID, "err", err)
			return
		}
		w.logins[inst.ID] = inst.AccountLogin
		for _, s := range scheds {
			current[s.ID] = s
		}
	}

	w.fresh = map[int64]bool{} // repo names are re-read once per tick (renames)

	// activeNow holds the schedules inside a window right now, for overlap.
	activeNow := map[int64]model.Schedule{}
	next := map[int64]*winState{}
	for id, s := range current {
		prev := w.state[id]
		win, err := engine.NextWindow(s, now)
		if err != nil {
			w.o.Logger.Warn("window watcher: bad schedule", "schedule", id, "err", err)
			if prev != nil {
				prev.sched = s
				next[id] = prev
			}
			continue
		}
		active := win != nil && win.Contains(now)
		if active {
			activeNow[id] = s
			w.warmRepo(ctx, s)
		}
		next[id] = &winState{active: active, sched: s}
	}

	var events []Event
	emit := func(kind Kind, s model.Schedule) bool {
		ev := w.event(ctx, kind, s)
		if kind == KindWindowEnd {
			ev.StillFrozenBy = overlap(activeNow, s)
		}
		if ev.Org == "" || (ev.Scope == "repo" && ev.Repo == "") {
			w.o.Logger.Warn("window watcher: event deferred, names unresolved", "kind", ev.Kind, "schedule", ev.Schedule)
			return false
		}
		events = append(events, ev)
		return true
	}
	for id, st := range next {
		prev := w.state[id]
		was := prev != nil && prev.active
		if !w.seeded || st.active == was {
			continue
		}
		kind := KindWindowEnd
		if st.active {
			kind = KindWindowStart
		}
		if !emit(kind, st.sched) {
			// Keep the old state so the next tick retries.
			if prev != nil {
				next[id] = prev
			} else {
				delete(next, id)
			}
		}
	}
	// Schedules that vanished or were disabled.
	for id, st := range w.state {
		if _, ok := current[id]; ok || !st.active || !w.seeded {
			continue
		}
		if !emit(KindWindowEnd, st.sched) {
			next[id] = st
		}
	}
	w.state = next
	w.seeded = true

	for _, ev := range events {
		w.o.Notifier.Notify(ev)
	}
}

// overlap names another active schedule that still covers the target of
// ended: for a repo schedule, an org-wide or same-repo one; for an org-wide
// schedule, another org-wide one. "" if none.
func overlap(active map[int64]model.Schedule, ended model.Schedule) string {
	best := ""
	for id, o := range active {
		if id == ended.ID || o.InstallationID != ended.InstallationID {
			continue
		}
		covers := o.RepoID == nil || (ended.RepoID != nil && *o.RepoID == *ended.RepoID)
		if covers && (best == "" || o.Name < best) {
			best = o.Name
		}
	}
	return best
}

// warmRepo refreshes the cached name of s's repo once per tick. On failure the
// last known name (if any) is kept.
func (w *WindowWatcher) warmRepo(ctx context.Context, s model.Schedule) {
	if s.RepoID == nil || w.fresh[*s.RepoID] {
		return
	}
	w.fresh[*s.RepoID] = true
	if r, err := w.o.Store.GetRepo(ctx, *s.RepoID); err == nil {
		w.repos[*s.RepoID] = r.Owner + "/" + r.Name
	}
}

func (w *WindowWatcher) event(ctx context.Context, kind Kind, s model.Schedule) Event {
	ev := Event{
		InstallationID: s.InstallationID, Kind: kind, Org: w.logins[s.InstallationID], Scope: "org",
		Schedule: s.Name, Reason: s.Reason,
	}
	ev.DashboardURL = w.link("/orgs/" + ev.Org)
	if s.RepoID != nil {
		w.warmRepo(ctx, s)
		ev.Scope, ev.Repo = "repo", w.repos[*s.RepoID]
		ev.DashboardURL = w.link("/repos/" + ev.Repo)
	}
	return ev
}

func (w *WindowWatcher) link(path string) string {
	if w.o.DashboardURL == "" {
		return ""
	}
	return strings.TrimRight(w.o.DashboardURL, "/") + path
}
