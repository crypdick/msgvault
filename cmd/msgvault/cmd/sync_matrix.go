package cmd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/matrix"
	"go.kenn.io/msgvault/internal/store"
)

func newSyncMatrixCmd() *cobra.Command {
	var opts matrix.ImportOptions
	cmd := &cobra.Command{
		Use:   "sync-matrix",
		Short: "Archive text from explicitly selected joined Matrix rooms",
		Long: `Archive Matrix text, notice, and emote messages using the built-in Matrix client.

Set up a dedicated device session with add-matrix first.
The first run backfills room history; later runs read new events. Page cursors
survive interruption. Encrypted pages require the account's decryption keys.
Media, edits, reactions, and redaction reconciliation are not supported yet.`,
		Args: cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if opts.MaxPages < 0 {
				return usageErr(cmd, errors.New("--max-pages must be non-negative"))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isDaemonCLISubprocess() {
				return runDaemonCLICommandHTTPFromCobra(cmd, args)
			}
			return runSyncMatrix(cmd, opts)
		},
	}
	cmd.Flags().StringSliceVar(&opts.RoomIDs, "room", nil, "joined Matrix room ID (repeat for several rooms)")
	cmd.Flags().IntVar(&opts.MaxPages, "max-pages", 0, "maximum history pages this run (0 = unlimited; rerun to resume)")
	cmd.Flags().BoolVar(&opts.Full, "full", false, "re-read selected rooms from the newest event, repairing existing rows")
	_ = cmd.MarkFlagRequired("room")
	return addManualSyncCacheFlags(cmd)
}

func runSyncMatrix(cmd *cobra.Command, opts matrix.ImportOptions) error {
	state := invocationFromCommand(cmd)
	if state == nil || state.cfg == nil {
		return errors.New("configuration is unavailable")
	}
	ctx, stop := withInterruptCancel(cmd, "\nInterrupted. Saving Matrix checkpoint...")
	defer stop()
	st, cleanup, err := openWritableStoreAndInitForIngestInvocation(state)
	if err != nil {
		return err
	}
	defer cleanup()
	sum, importErr := syncMatrixArchive(ctx, st, state.cfg.TokensDir(), opts)
	if sum != nil {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Matrix: %d pages, %d added, %d updated, %d unsupported events skipped\n",
			sum.PagesProcessed, sum.MessagesAdded, sum.MessagesUpdated, sum.EventsSkipped)
		if !sum.HistoryComplete && importErr == nil {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "History backfill paused; rerun sync-matrix to resume.")
		}
	}
	return errors.Join(importErr, runPostSourceCreateMigrationsForInvocation(st, state),
		rebuildCacheAfterManualSync(state.cfg.DatabaseDSN(), state))
}

// Manual commands require explicit rooms. Scheduled runs pass an empty list
// to discover all joined chats, including rooms joined since the previous run.
func syncMatrixArchive(ctx context.Context, st *store.Store, tokensDir string, opts matrix.ImportOptions) (*matrix.ImportSummary, error) {
	client, err := matrix.NewClient(ctx, filepath.Join(tokensDir, "matrix"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = client.Close() }()
	if err := client.SyncKeys(ctx); err != nil {
		return nil, err
	}
	if len(opts.RoomIDs) == 0 {
		opts.RoomIDs, err = client.ChatRoomIDs(ctx)
		if err != nil {
			return nil, err
		}
		if len(opts.RoomIDs) == 0 {
			return &matrix.ImportSummary{HistoryComplete: true}, nil
		}
	}
	return matrix.Import(ctx, st, client.Account(), client.Page, opts)
}

func init() {
	rootCmd.AddCommand(newSyncMatrixCmd())
}
