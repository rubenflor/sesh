package seshcli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joshmedeski/sesh/v2/git"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestCurrentSessionCallersUseVerifiedInventoryMetadata(t *testing.T) {
	for _, caller := range []string{"root", "implicit window connect"} {
		t.Run(caller, func(t *testing.T) {
			t.Setenv("TMUX", "/tmp/tmux-fixture,101,1")
			const generation = "101:1700000000:/tmp/tmux-fixture"
			const identityFormat = "#{session_id}::#{pid}:#{start_time}:#{socket_path}"
			dir := t.TempDir()
			pathA, pathB := filepath.Join(dir, "A"), filepath.Join(dir, "B")
			require.NoError(t, os.Mkdir(pathA, 0700))
			require.NoError(t, os.Mkdir(pathB, 0700))
			configPath := filepath.Join(dir, "sesh.toml")
			require.NoError(t, os.WriteFile(configPath, []byte("cache = false\n"), 0600))
			s := shell.NewMockShell(t)
			rows := []string{
				"200::::1::::100::0::::0::::::0::0::0::$1::200::0::0::a::" + pathA + "::::3::" + generation,
				"100::::1::::100::0::::0::::::0::0::0::$2::100::0::0::b::" + pathB + "::::1::" + generation,
			}
			inventory := s.EXPECT().ListCmd("tmux", "list-sessions", "-F", mock.Anything).Return(rows, nil).Once()
			// The current display name has become b; only ID $1 is authoritative.
			s.EXPECT().Cmd("tmux", "display-message", "-p", identityFormat).Return("$1::"+generation, nil).Once().NotBefore(inventory)
			s.EXPECT().Cmd("tmux", "display-message", "-p", "-t", "$1:", identityFormat).Return("$1::"+generation, nil).Once()
			inventoryReads := 1
			if caller == "implicit window connect" {
				// Revalidation retains the selected ID and its session root.
				s.EXPECT().ListCmd("tmux", "list-sessions", "-F", mock.Anything).Return(rows, nil).Once()
				inventoryReads++
				s.EXPECT().CmdCapture("tmux", "if-shell", "-F", "-t", "$1:", mock.Anything,
					"'new-window' '-P' '-F' '#{session_name}:#{window_index}' '-c' "+shell.Quote(pathA)+" '-d' '-t' '$1:'", "if-shell").Return("b:1", nil).Once()
			}
			s.EXPECT().CmdCapture("tmux", "show-environment", "-t", "$1", "--", model.SSHBridgeIdentityEnv).Return("", nil).Times(inventoryReads + 1)
			s.EXPECT().CmdCapture("tmux", "show-environment", "-t", "$2", "--", model.SSHBridgeIdentityEnv).Return("", nil).Times(inventoryReads)
			base := NewBaseDeps()
			base.Shell = s
			root := &cobra.Command{Use: "sesh"}
			root.PersistentFlags().String("config", configPath, "")
			if caller == "root" {
				resolvedA, err := filepath.EvalSymlinks(pathA)
				require.NoError(t, err)
				g := git.NewMockGit(t)
				g.EXPECT().WorktreeList(resolvedA).Return(false, "", nil).Once()
				base.Git = g
				root.AddCommand(NewRootSessionCommand(base))
				root.SetArgs([]string{"root"})
			} else {
				root.AddCommand(NewWindowCommand(base))
				root.SetArgs([]string{"window", "connect", "--new", "--background"})
			}

			require.NoError(t, root.Execute())
		})
	}
}
