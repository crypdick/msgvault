package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/config"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestMatrixSourceReportsItsGenericSchedulerJob(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	st := testutil.NewTestStore(t)
	_, err := st.GetOrCreateSource("matrix", "@alice:example.com at https://matrix.example.com")
	require.NoError(err)
	sched := newMockScheduler()
	sched.jobStatuses = []JobStatus{{Name: MatrixJobName, Schedule: "*/5 * * * *", Running: true, Pending: true}}
	srv := NewServer(&config.Config{Server: config.ServerConfig{APIPort: 8080}}, st, sched, testLogger())
	status := sourceStatusFor(t, srv)
	assert.True(status.SchedulerPending)
	assert.Equal("*/5 * * * *", status.Schedule)
}
