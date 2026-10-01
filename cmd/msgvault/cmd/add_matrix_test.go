package cmd

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/clirun"
)

func TestMatrixSetupForwardsCredentialsThroughDaemon(t *testing.T) {
	for _, test := range []struct {
		name    string
		command func() *cobra.Command
		args    []string
		want    []string
		env     string
	}{
		{"login", newAddMatrixCmd,
			[]string{"--homeserver", "https://matrix.example.com", "--user", "@alice:example.com", "--password-stdin"},
			[]string{"add-matrix", "--homeserver=https://matrix.example.com", "--password-stdin", "--user=@alice:example.com"}, clirun.EnvMatrixPassword},
		{"recovery", newRecoverMatrixCmd, []string{"--secret-stdin"},
			[]string{"recover-matrix", "--secret-stdin"}, clirun.EnvMatrixRecoveryKey},
	} {
		t.Run(test.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			t.Setenv(test.env, "")
			server, requests := newDaemonCLIRunnerTestServer(t, func(request daemonCLIRunTestRequest) {
				assert.Equal(test.want, request.Args)
				assert.Equal(map[string]string{test.env: "synthetic secret"}, request.Env)
			}, `{"type":"stdout","data":"Matrix ready\n"}`, `{"type":"complete"}`)
			command := test.command()
			command.SetContext(configureRemoteDaemonForTest(t, server.URL))
			var output bytes.Buffer
			command.SetOut(&output)
			command.SetErr(&output)
			command.SetIn(strings.NewReader("synthetic secret\n"))
			command.SetArgs(test.args)
			require.NoError(command.Execute())
			assert.EqualValues(1, requests.Load())
			assert.Contains(output.String(), "Matrix ready")
			assert.NotContains(output.String(), "synthetic secret")
		})
	}
}

func TestMatrixSecretsRejectMissingOrOversizedStdin(t *testing.T) {
	t.Setenv(clirun.EnvMatrixRecoveryKey, "")
	for _, input := range []string{"", strings.Repeat("x", (64<<10)+1)} {
		command := newRecoverMatrixCmd()
		command.SetIn(strings.NewReader(input))
		command.SetOut(io.Discard)
		command.SetErr(io.Discard)
		command.SetArgs([]string{"--secret-stdin"})
		require.ErrorContains(t, command.Execute(), "empty or exceeds 64 KiB")
	}
}
