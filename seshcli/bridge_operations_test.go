package seshcli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/dir"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmuxinator"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Use the real CLI dependency graph, mocking only external commands and the
// directory check. Unexpected bridge captures or mutations fail the test.
func bridgeOperationsBase(t *testing.T, marker string) (*BaseDeps, string) {
	t.Helper()
	t.Setenv("TMUX", "/tmp/sesh-test,1,1")
	configPath := filepath.Join(t.TempDir(), "sesh.toml")
	require.NoError(t, os.WriteFile(configPath, nil, 0600))
	s := shell.NewMockShell(t)
	const generation = "101:1700000000:/tmp/tmux-fixture"
	const identityFormat = "#{session_id}::#{pid}:#{start_time}:#{socket_path}"
	s.On("Cmd", "tmux", "display-message", "-p", identityFormat).Return("$1::"+generation, nil).Maybe()
	s.On("Cmd", "tmux", "display-message", "-p", "-t", "$1:", identityFormat).Return("$1::"+generation, nil).Maybe()
	s.On("CmdCaptureRaw", mock.Anything, "tmux", 16<<20, 64<<10, "-u", "list-sessions", "-F", mock.Anything).Return(rawInventoryResult(strings.Join([]string{
		sessionInventoryRow([]string{"100", "", "1", "", "100", "0", "", "0", "", "", "0", "0", "0", "$1", "100", "0", "0", "renamed-bridge", "/bridge", "", "1", generation}),
		sessionInventoryRow([]string{"200", "", "1", "", "100", "0", "", "0", "", "", "0", "0", "0", "$2", "200", "0", "0", "other-client", "/local", "", "1", generation}),
	}, "\n")), nil)
	s.On("CmdCapture", "tmux", "show-environment", "-t", "$1", "--", model.SSHBridgeIdentityEnv).
		Return(model.SSHBridgeIdentityEnv+"="+marker, nil)
	s.On("CmdCapture", "tmux", "show-environment", "-t", "$2", "--", model.SSHBridgeIdentityEnv).
		Return("", nil)
	base := NewBaseDeps()
	base.Shell = s
	base.Tmuxinator = tmuxinator.NewTmuxinator(s)
	return base, configPath
}

func TestLocalCommandsCannotUseSSHBridge(t *testing.T) {
	for _, tt := range []struct {
		name      string
		command   func(*BaseDeps) *cobra.Command
		args      []string
		errorText string
	}{
		{"root", NewRootSessionCommand, nil, "No root found"},
		{"last", NewLastCommand, nil, "No last session found"},
		{"rename attached", NewRenameCommand, []string{"--enrich"}, ""},
		{"rename explicit", NewRenameCommand, []string{"--enrich", "renamed-bridge"}, ""},
		{"preview", NewPreviewCommand, []string{"renamed-bridge"}, ""},
		{"window list attached", NewWindowCommand, []string{"list"}, "use --target"},
		{"window list explicit", NewWindowCommand, []string{"list", "--target", "renamed-bridge"}, "not found"},
		{"window list JSON", NewWindowCommand, []string{"list", "--json", "--target", "renamed-bridge"}, "not found"},
		{"window connect attached", NewWindowCommand, []string{"connect", "notes"}, "use --target"},
		{"window connect explicit", NewWindowCommand, []string{"connect", "notes", "--target", "renamed-bridge"}, "SSH bridge"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			base, configPath := bridgeOperationsBase(t, "ssh/host/work")
			if tt.name == "window connect explicit" {
				require.NoError(t, os.WriteFile(configPath, []byte("[[session]]\nname = 'renamed-bridge'\npath = '/local'\n"), 0600))
				base.Shell.(*shell.MockShell).On("ListCmd", "tmuxinator", "list", "-n").Return(nil, assert.AnError)
			}
			if tt.name == "preview" {
				d := dir.NewMockDir(t)
				d.EXPECT().Dir("renamed-bridge").Return(false, "")
				base.Dir = d
			}
			root := &cobra.Command{Use: "sesh", SilenceErrors: true, SilenceUsage: true}
			root.PersistentFlags().String("config", configPath, "")
			cmd := tt.command(base)
			root.AddCommand(cmd)
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(append([]string{cmd.Name()}, tt.args...))

			err := root.Execute()

			if tt.errorText != "" {
				require.ErrorContains(t, err, tt.errorText)
			} else {
				require.NoError(t, err)
			}
			assert.Empty(t, out.String())
		})
	}
}

func TestWindowListAllowsUnmarkedLocalSession(t *testing.T) {
	for _, args := range [][]string{{"list"}, {"list", "--target", "renamed-bridge"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			base, configPath := bridgeOperationsBase(t, "")
			s := base.Shell.(*shell.MockShell)
			s.On("CmdCaptureRaw", mock.Anything, "tmux", 16<<20, 64<<10, "-u", "if-shell", "-F", "-t", "$1:", mock.Anything,
				`'list-windows' '-t' '$1' '-F' 'W1::#{session_id}::"#{q:session_name}"::#{window_id}::#{window_index}::#{window_active}::"#{q:window_name}"::"#{q:pane_current_path}"::"#{pid}:#{start_time}:#{q:socket_path}"'`, "if-shell").Return(rawInventoryResult(`W1::$1::"user"::@7::4::1::"notes"::"/user"::"101:1700000000:/tmp/socket"`), nil).Once()
			root := &cobra.Command{Use: "sesh"}
			root.PersistentFlags().String("config", configPath, "")
			root.AddCommand(NewWindowCommand(base))
			root.SetArgs(append([]string{"window"}, args...))

			require.NoError(t, root.Execute())
		})
	}
}
