package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/TheJokersThief/frozen-throne/internal/engine"
	"github.com/TheJokersThief/frozen-throne/internal/model"
)

func oneOff(t *testing.T, e *env, inst int64, repo *int64, from, to time.Time) {
	t.Helper()
	_, err := e.st.CreateSchedule(context.Background(), model.Schedule{
		InstallationID: inst, RepoID: repo, Name: "w", Kind: model.ScheduleOneOff,
		Timezone: "UTC", Enabled: true, StartsAt: &from, EndsAt: &to,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerFiresAtWindowBoundaries(t *testing.T) {
	e := newEnv(t, map[int64]int{1: 2}, func(c *Config) { c.Engine = engine.New() })
	repo := int64(1001)
	oneOff(t, e, 1, &repo, start0.Add(time.Hour), start0.Add(2*time.Hour))
	e.run(t)
	e.r.All()
	eventually(t, func() bool { return e.ev.count(1001) == 1 && e.ev.count(1002) == 1 })
	eventually(t, func() bool { return e.clk.Pending() == 1 })

	e.clk.Advance(time.Hour) // window start; only the scoped repo is affected
	eventually(t, func() bool { return e.ev.count(1001) == 2 })
	consistently(t, func() bool { return e.ev.count(1002) == 1 })
	eventually(t, func() bool { return e.clk.Pending() == 1 })

	e.clk.Advance(time.Hour) // window end
	eventually(t, func() bool { return e.ev.count(1001) == 3 })
	consistently(t, func() bool { return e.ev.count(1002) == 1 && e.ev.count(1001) == 3 })
	eventually(t, func() bool { return e.clk.Pending() == 0 })
}

func TestSchedulerPicksUpScheduleAddedViaTrigger(t *testing.T) {
	e := newEnv(t, map[int64]int{1: 1}, func(c *Config) { c.Engine = engine.New() })
	e.run(t)
	e.r.All()
	eventually(t, func() bool { return e.ev.count(1001) == 1 })
	consistently(t, func() bool { return e.clk.Pending() == 0 })

	oneOff(t, e, 1, nil, start0.Add(time.Hour), start0.Add(2*time.Hour))
	e.r.Installation(1) // what the API does after a schedule edit
	eventually(t, func() bool { return e.clk.Pending() == 1 })
	e.clk.Advance(time.Hour)
	eventually(t, func() bool { return e.ev.count(1001) == 3 })
}
