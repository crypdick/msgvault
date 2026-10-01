package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCLIRunCommandAllowedMatrix(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"add-matrix", "recover-matrix", "matrix-rooms", "sync-matrix"} {
		t.Run(command, func(t *testing.T) {
			assert.True(t, cliRunCommandAllowed([]string{command}))
		})
	}
}
