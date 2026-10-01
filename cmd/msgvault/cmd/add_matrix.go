package cmd

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"go.kenn.io/msgvault/internal/clirun"
	"go.kenn.io/msgvault/internal/matrix"
)

func newAddMatrixCmd() *cobra.Command {
	var account matrix.Account
	var passwordStdin bool
	cmd := &cobra.Command{
		Use: "add-matrix", Short: "Log in with a dedicated Matrix archive device",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			password, err := matrixSecret(cmd, clirun.EnvMatrixPassword, passwordStdin)
			if err != nil {
				return err
			}
			if !isDaemonCLISubprocess() {
				return runDaemonCLICommandHTTPFromCobraWithEnv(cmd, args, map[string]string{clirun.EnvMatrixPassword: password})
			}
			state := invocationFromCommand(cmd)
			if state == nil || state.cfg == nil {
				return errors.New("configuration is unavailable")
			}
			client, err := matrix.Login(cmd.Context(), filepath.Join(state.cfg.TokensDir(), "matrix"), account, password)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			st, cleanup, err := openWritableStoreAndInitForIngestInvocation(state)
			if err != nil {
				return err
			}
			defer cleanup()
			if _, err := st.GetOrCreateSource("matrix", client.Account().Identifier()); err != nil {
				return err
			}
			if err := runPostSourceCreateMigrationsForInvocation(st, state); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Matrix account connected. Run matrix-rooms to select rooms; recover-matrix restores encrypted history keys.")
			return nil
		},
	}
	cmd.Flags().StringVar(&account.Homeserver, "homeserver", "", "Matrix homeserver URL")
	cmd.Flags().StringVar(&account.UserID, "user", "", "full Matrix user ID")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read the password from standard input")
	_ = cmd.MarkFlagRequired("homeserver")
	_ = cmd.MarkFlagRequired("user")
	return cmd
}

func newRecoverMatrixCmd() *cobra.Command {
	var secretStdin bool
	cmd := &cobra.Command{
		Use: "recover-matrix", Short: "Recover Matrix room keys from an authenticated server backup",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			secret, err := matrixSecret(cmd, clirun.EnvMatrixRecoveryKey, secretStdin)
			if err != nil {
				return err
			}
			if !isDaemonCLISubprocess() {
				return runDaemonCLICommandHTTPFromCobraWithEnv(cmd, args, map[string]string{clirun.EnvMatrixRecoveryKey: secret})
			}
			client, err := matrixClientForCommand(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			count, err := client.Recover(cmd.Context(), secret)
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Matrix room keys recovered: %d\n", count)
			return err
		},
	}
	cmd.Flags().BoolVar(&secretStdin, "secret-stdin", false, "read recovery key or passphrase from standard input")
	return cmd
}

func newMatrixRoomsCmd() *cobra.Command {
	return &cobra.Command{
		Use: "matrix-rooms", Short: "List joined rooms for the Matrix archive account",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isDaemonCLISubprocess() {
				return runDaemonCLICommandHTTPFromCobra(cmd, args)
			}
			client, err := matrixClientForCommand(cmd)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			rooms, err := client.Rooms(cmd.Context())
			if err != nil {
				return err
			}
			if err := json.MarshalWrite(cmd.OutOrStdout(), rooms, json.Deterministic(true)); err != nil {
				return fmt.Errorf("write Matrix rooms: %w", err)
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout())
			if err != nil {
				return fmt.Errorf("write Matrix rooms newline: %w", err)
			}
			return nil
		},
	}
}

func matrixClientForCommand(cmd *cobra.Command) (*matrix.Client, error) {
	state := invocationFromCommand(cmd)
	if state == nil || state.cfg == nil {
		return nil, errors.New("configuration is unavailable")
	}
	return matrix.NewClient(cmd.Context(), filepath.Join(state.cfg.TokensDir(), "matrix"))
}

func matrixSecret(cmd *cobra.Command, envName string, fromStdin bool) (string, error) {
	if secret := os.Getenv(envName); secret != "" {
		return secret, nil
	}
	if !fromStdin || isDaemonCLISubprocess() {
		return "", fmt.Errorf("provide the Matrix secret on stdin or through %s", envName)
	}
	const maxSecretBytes = 64 << 10
	data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), maxSecretBytes+1))
	if err != nil {
		return "", fmt.Errorf("read Matrix secret: %w", err)
	}
	secret := strings.TrimRight(string(data), "\r\n")
	if len(data) > maxSecretBytes || secret == "" {
		return "", errors.New("matrix secret is empty or exceeds 64 KiB")
	}
	return secret, nil
}

func init() {
	rootCmd.AddCommand(newAddMatrixCmd(), newRecoverMatrixCmd(), newMatrixRoomsCmd())
}
