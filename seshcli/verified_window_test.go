package seshcli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestWindowListUsesVerifiedSessionID(t *testing.T) {
	for _, name := range []string{"$0", "$1", "$2", "ordinary", "sesh-ssh-user"} {
		for _, jsonOutput := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/text", true: "/json"}[jsonOutput], func(t *testing.T) {
				config := filepath.Join(t.TempDir(), "sesh.toml")
				const generation = "101:1700000000:/tmp/tmux-fixture"
				require.NoError(t, os.WriteFile(config, nil, 0600))
				s := shell.NewMockShell(t)
				s.EXPECT().ListCmd("tmux", "list-sessions", "-F", mock.Anything).Return([]string{
					strings.Join([]string{"100", "", "0", "", "100", "0", "", "0", "", "", "0", "0", "0", "$8", "100", "0", "0", name, "/user", "", "1", generation}, "::"),
					strings.Join([]string{"100", "", "0", "", "100", "0", "", "0", "", "", "0", "0", "0", "$2", "100", "0", "0", "bridge", "/bridge", "", "1", generation}, "::"),
				}, nil)
				s.EXPECT().CmdCapture("tmux", "show-environment", "-t", "$8", "--", model.SSHBridgeIdentityEnv).Return("", nil)
				s.EXPECT().CmdCapture("tmux", "show-environment", "-t", "$2", "--", model.SSHBridgeIdentityEnv).Return(model.SSHBridgeIdentityEnv+"=ssh/host/work", nil)
				s.EXPECT().ListCmd("tmux", "-u", "if-shell", "-F", "-t", "$8:", mock.Anything,
					"'list-windows' '-t' '$8' '-F' '#{window_id}::#{window_index}::#{window_active}::#{n:window_name}::#{n:pane_current_path}::#{n:#{pid}:#{start_time}:#{socket_path}}::#{window_name}#{pane_current_path}#{pid}:#{start_time}:#{socket_path}'", "if-shell").Return(nil, nil).Once()
				base := NewBaseDeps()
				base.Shell = s
				root := &cobra.Command{Use: "sesh"}
				root.PersistentFlags().String("config", config, "")
				root.AddCommand(NewWindowCommand(base))
				args := []string{"window", "list", "--target", name}
				if jsonOutput {
					args = append(args, "--json")
				}
				root.SetArgs(args)

				require.NoError(t, root.Execute())
			})
		}
	}
}
