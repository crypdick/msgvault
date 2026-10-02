package cmd

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/scheduler"
)

func TestMatrixSchedulerHonorsEnabledScheduleAndRuns(t *testing.T) {
	for _, test := range []struct {
		name    string
		enabled bool
		cron    string
		want    bool
	}{
		{"disabled", false, "*/5 * * * *", false},
		{"manual", true, "", false},
		{"scheduled", true, "*/5 * * * *", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			sched := scheduler.New(nil)
			t.Cleanup(func() { sched.Stop() })
			runs := make(chan struct{}, 1)
			cfg := config.MatrixConfig{Enabled: test.enabled, Schedule: test.cron, MaxPages: 10}
			require.NoError(registerMatrixSchedulerJob(sched, cfg, func(context.Context) error {
				runs <- struct{}{}
				return nil
			}))
			assert.Equal(test.want, sched.IsJobScheduled("matrix"))
			if !test.want {
				return
			}
			sched.Start()
			require.NoError(sched.TriggerJob("matrix"))
			select {
			case <-runs:
			case <-time.After(5 * time.Second):
				require.Fail("scheduled Matrix sync did not run")
			}
		})
	}
}

func TestMatrixSchedulerRejectsInvalidScheduleAndUnlimitedPass(t *testing.T) {
	for _, cfg := range []config.MatrixConfig{
		{Enabled: true, Schedule: "invalid", MaxPages: 10},
		{Enabled: true, Schedule: "*/5 * * * *", MaxPages: 0},
	} {
		sched := scheduler.New(nil)
		t.Cleanup(func() { sched.Stop() })
		require.Error(t, registerMatrixSchedulerJob(sched, cfg, func(context.Context) error { return nil }))
	}
}
