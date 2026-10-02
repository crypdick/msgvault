package cmd

import (
	"context"
	"errors"

	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/matrix"
	"go.kenn.io/msgvault/internal/scheduler"
	"go.kenn.io/msgvault/internal/store"
)

func registerMatrixSchedulerJob(sched *scheduler.Scheduler, cfg config.MatrixConfig, run func(context.Context) error) error {
	if !cfg.Enabled || cfg.Schedule == "" {
		return nil
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	return sched.AddJob(scheduler.Job{
		Name: api.MatrixJobName, Schedule: cfg.Schedule, Preemptible: true, Run: run,
	})
}

func runScheduledMatrixSync(ctx context.Context, st *store.Store) error {
	state := invocationFromContext(ctx)
	if state == nil || state.cfg == nil {
		return errors.New("configuration is unavailable")
	}
	cfg := state.cfg
	sum, importErr := syncMatrixArchive(ctx, st, cfg.TokensDir(), matrix.ImportOptions{
		RoomIDs: cfg.Matrix.Rooms, MaxPages: cfg.Matrix.MaxPages,
	})
	if sum != nil {
		repairLogger(state.logger).Info("Matrix sync pass", "pages", sum.PagesProcessed,
			"messages_added", sum.MessagesAdded, "messages_updated", sum.MessagesUpdated,
			"history_complete", sum.HistoryComplete)
	}
	if scheduledSyncYielded(ctx) {
		return scheduledSyncYieldResult(ctx, importErr)
	}
	return errors.Join(importErr, runPostSourceCreateMigrationsForInvocation(st, state),
		rebuildCacheAfterScheduledSync(ctx, api.MatrixJobName))
}
