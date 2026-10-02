package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatrixScheduleLoadsWithBoundedDefault(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	require.NoError(os.WriteFile(path, []byte("[matrix]\nenabled = true\nschedule = '*/5 * * * *'\n"), 0o600))
	cfg, err := Load(path, "")
	require.NoError(err)
	assert.True(cfg.Matrix.Enabled)
	assert.Equal("*/5 * * * *", cfg.Matrix.Schedule)
	assert.Equal(100, cfg.Matrix.MaxPages)
	assert.Empty(cfg.Matrix.Rooms)
	assert.False(NewDefaultConfig().Matrix.Enabled)
}

func TestMatrixScheduleRejectsUnboundedPass(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[matrix]\nenabled = true\nschedule = '*/5 * * * *'\nmax_pages = 0\n"), 0o600))
	_, err := Load(path, "")
	require.ErrorContains(t, err, "matrix.max_pages must be positive")
}
