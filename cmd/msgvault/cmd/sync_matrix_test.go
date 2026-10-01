package cmd

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSyncMatrixRequiresRooms(t *testing.T) {
	command := newSyncMatrixCmd()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs(nil)
	require.ErrorContains(t, command.ExecuteContext(context.Background()), `required flag(s) "room" not set`)
}

func TestSyncMatrixRejectsNegativePageLimit(t *testing.T) {
	command := newSyncMatrixCmd()
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	command.SetArgs([]string{"--room", "!chat:example.com", "--max-pages=-1"})
	require.ErrorContains(t, command.ExecuteContext(context.Background()), "--max-pages must be non-negative")
}

func TestSyncMatrixUsesDaemonRunner(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	server, requests := newDaemonCLIRunnerTestServer(t, func(req daemonCLIRunTestRequest) {
		assert.Equal([]string{"sync-matrix", "--full", "--max-pages=2",
			"--room=!chat:example.com", "--room=!other:example.com"}, req.Args)
	}, `{"type":"stdout","data":"Matrix sync complete\n"}`, `{"type":"complete"}`)
	testCtx := configureRemoteDaemonForTest(t, server.URL)
	var output bytes.Buffer
	command := newSyncMatrixCmd()
	command.SetContext(testCtx)
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{"--room", "!chat:example.com", "--room", "!other:example.com", "--max-pages=2", "--full"})
	require.NoError(command.Execute())
	assert.EqualValues(1, requests.Load())
	assert.Contains(output.String(), "Matrix sync complete")
	assert.True(manualSyncCLICommand([]string{"sync-matrix"}))
}
