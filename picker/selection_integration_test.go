//go:build unix

package picker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/joshmedeski/sesh/v2/cache"
	"github.com/joshmedeski/sesh/v2/connector"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/oswrap"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/zoxide"
	"github.com/stretchr/testify/require"
)

type selectionPrivateShell struct {
	*deletionPrivateShell
	t       *testing.T
	focused []string
}

// Replace only interactive attach with a native ID query in the same generation
// guard. Inventory, ownership, rename, restart and capture are real tmux commands.
func (s *selectionPrivateShell) CmdWithOutput(_ string, args ...string) (string, error) {
	s.t.Helper()
	require.Len(s.t, args, 7)
	require.Equal(s.t, "if-shell", args[0])
	require.True(s.t, strings.HasPrefix(args[5], "'attach-session' "))
	_, rest, found := strings.Cut(args[5], "'-t' '")
	require.True(s.t, found)
	id, _, found := strings.Cut(rest, "'")
	require.True(s.t, found)
	guarded := append([]string(nil), args...)
	guarded[5] = "display-message -p -t " + shell.Quote(id+":") + " '#{session_id}'"
	out, err := s.run(guarded...)
	if err == nil {
		s.focused = append(s.focused, out)
	}
	return out, err
}

func TestPickerSelectionIdentityOnPrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	for _, name := range []string{"work", "$0"} {
		for _, change := range []string{"normal", "rename", "restart"} {
			for _, cached := range []bool{false, true} {
				source := "live"
				if cached {
					source = "cached"
				}
				t.Run(name+"/"+change+"/"+source, func(t *testing.T) {
					t.Setenv("TMPDIR", "/tmp")
					t.Setenv(model.SSHBridgeIdentityEnv, "")
					sh := &selectionPrivateShell{deletionPrivateShell: newDeletionPrivateShell(t, binary), t: t}
					canonicalDir, err := filepath.EvalSymlinks(sh.dir)
					require.NoError(t, err)
					sh.socket = filepath.Join(canonicalDir, "socket")
					run := func(args ...string) string {
						t.Helper()
						out, err := sh.run(args...)
						require.NoError(t, err)
						return out
					}
					start := func() int {
						t.Helper()
						created := strings.Fields(run("new-session", "-d", "-P", "-F", "#{pid} #{session_id}", "-s", "bridge", "--", "/bin/sh", "-c", "printf 'BRIDGE_OUTPUT\\n'; exec sleep 60"))
						require.Len(t, created, 2)
						pid := sh.recordPID(t, created[0])
						require.Equal(t, "$0", created[1])
						run("set-environment", "-t", "$0", model.SSHBridgeIdentityEnv, "ssh/fixture/bridge")
						return pid
					}
					create := func(id, sessionName, output string) {
						t.Helper()
						require.Equal(t, id, run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", sessionName, "--", "/bin/sh", "-c", "printf '"+output+"\\n'; exec sleep 60"))
						require.Eventually(t, func() bool {
							return strings.Contains(run("capture-pane", "-p", "-t", id+":"), output) &&
								strings.Contains(run("capture-pane", "-p", "-t", "$0:"), "BRIDGE_OUTPUT")
						}, 2*time.Second, 10*time.Millisecond)
					}
					state := func(id string) []string {
						return []string{
							run("display-message", "-p", "-t", id+":", "#{session_id} #{session_name} #{pane_id} #{pane_pid}"),
							run("list-windows", "-t", id+":", "-F", "#{window_id} #{window_active} #{pane_id} #{pane_active}"),
							run("show-environment", "-t", id),
							run("capture-pane", "-p", "-t", id+":"),
						}
					}
					oldPID := start()
					create("$1", name, "ORIGINAL_OUTPUT")
					osw := oswrap.NewMockOs(t)
					osw.EXPECT().Getenv("TMUX").Return("").Maybe()
					tm := tmux.NewTmux(osw, sh, binary)
					live := lister.NewLister(model.Config{}, nil, tm, nil, nil, nil, nil, nil)
					sessions, err := live.List(lister.ListOptions{Tmux: true})
					require.NoError(t, err)
					require.Len(t, sessions.OrderedIndex, 1, "bridge remains hidden")
					original := sessions.Directory[sessions.OrderedIndex[0]]
					require.Equal(t, "$1", original.TmuxSessionID)
					require.NotEmpty(t, original.TmuxServerGeneration)
					cachePath := filepath.Join(sh.dir, "sessions.gob")
					var cacheBefore []byte
					if cached {
						fc := cache.NewFileCacheWithPath(cachePath)
						require.NoError(t, fc.Write(sessions))
						stored, err := fc.Read()
						require.NoError(t, err)
						sessions = stored.Sessions
						cacheBefore, err = os.ReadFile(cachePath)
						require.NoError(t, err)
					}
					p := &RealPicker{tmux: tm}
					m := New(testFetchFunc(sessions), testOptionsWith(func(o *Options) {
						o.Preview, o.PreviewFunc = true, p.previewSession
					}))
					loaded, previewCmd := m.Update(sessionsLoadedMsg{sessions: sessions})
					selected, _ := loaded.(Model).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
					row := selected.(Model).ChosenSession()
					require.Equal(t, original, row, "picker handoff retains the captured tuple")
					protectedID := "$0"
					switch change {
					case "rename":
						run("rename-session", "-t", "$1:", "renamed")
						create("$2", name, "REPLACEMENT_OUTPUT")
						protectedID = "$2"
					case "restart":
						run("kill-server")
						require.Eventually(t, func() bool { return syscall.Kill(oldPID, 0) == syscall.ESRCH }, 3*time.Second, 10*time.Millisecond)
						start()
						create("$1", name, "REPLACEMENT_OUTPUT")
						protectedID = "$1"
						require.NotEqual(t, row.TmuxServerGeneration, run("display-message", "-p", "-t", "$1:", "#{pid}:#{start_time}:#{socket_path}"))
					}
					bridgeBefore, protectedBefore := state("$0"), state(protectedID)
					z := zoxide.NewMockZoxide(t)
					z.EXPECT().Add(row.Path).Return(nil).Once()
					c := connector.NewConnector(model.Config{}, nil, nil, lister.NewMockLister(t), nil, nil, tm, z, nil, nil)
					_, connectErr := c.ConnectSession(row, model.ConnectOpts{})
					m = loadPreview(t, loaded.(Model), previewCmd)
					if change == "restart" {
						require.Error(t, connectErr)
						require.Error(t, m.previewErr)
						require.Empty(t, m.previewContent)
						require.Empty(t, sh.focused)
					} else {
						require.NoError(t, connectErr)
						require.NoError(t, m.previewErr)
						require.Equal(t, []string{"$1"}, sh.focused)
						require.Contains(t, m.previewContent, "ORIGINAL_OUTPUT")
						require.NotContains(t, m.previewContent, "REPLACEMENT_OUTPUT")
						require.NotContains(t, m.previewContent, "BRIDGE_OUTPUT")
					}
					require.Equal(t, bridgeBefore, state("$0"), "bridge name, marker, selection and output remain untouched")
					require.Equal(t, protectedBefore, state(protectedID), "unrelated session remains untouched")
					if cached {
						cacheAfter, err := os.ReadFile(cachePath)
						require.NoError(t, err)
						require.Equal(t, cacheBefore, cacheAfter)
					}
				})
			}
		}
	}
}
