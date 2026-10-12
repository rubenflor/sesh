//go:build unix

package tmux_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/require"
)

// SSH is a local transport stub; every tmux invocation uses only this private socket.
func sshWindowPrivateTransport(t *testing.T, s *inventoryPrivateShell, socket string) shell.Shell {
	t.Helper()
	bin := t.TempDir()
	ssh := `#!/bin/sh
[ "$#" -eq 7 ] || exit 90
[ "$1" = -T ] && [ "$2" = -n ] && [ "$3" = -o ] && [ "$4" = BatchMode=yes ] && [ "$5" = -- ] && [ "$6" = dev ] || exit 91
exec /bin/sh -c "$7"
`
	query := `#!/bin/sh
[ "$1" = -N ] && [ "$2" = -u ] || exit 92
case "$3" in list-sessions|display-message|list-windows) ;; *) exit 93 ;; esac
exec ` + shell.Quote(s.binary) + " -S " + shell.Quote(socket) + " -f /dev/null \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ssh"), []byte(ssh), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "tmux"), []byte(query), 0700))
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("TMUX", "")
	t.Setenv("LC_ALL", "C")
	return shell.NewShell(execwrap.NewExec(), nil)
}

func TestSSHWindowDiscoveryOnPrivateTmux(t *testing.T) {
	s := newInventoryPrivateShell(t)
	t.Chdir(s.dir)
	config := model.Config{SSH: model.SSHConfig{Hosts: []string{"dev"}}}
	path := filepath.Join(s.dir, "dir '\" $LITERAL% \\n\t雪::\nnext")
	require.NoError(t, os.Mkdir(path, 0700))
	const windowName = "dup '\" \\n /% café雪"
	const storedWindowName = "dup '\" \\\\n /% café雪"
	for _, name := range []string{"$0", "a/b% café雪", "quote';$(touch MUST_NOT_RUN);#{session_id}"} {
		t.Run(name, func(t *testing.T) {
			// Creation accepts a format; discovery must receive the literal stored name.
			id := s.must("-u", "new-session", "-d", "-P", "-F", "#{session_id}", "-s", strings.ReplaceAll(name, "#", "##"), "-n", windowName, "-c", path, "--", "/bin/cat")
			require.Equal(t, model.TmuxSSHSessionTarget(name), s.must("-u", "display-message", "-p", "-t", id+":", "#{session_name}"), "fixture must preserve its requested session name")
			require.Equal(t, storedWindowName, s.must("-u", "display-message", "-p", "-t", id+":", "#{window_name}"))
			first := s.must("display-message", "-p", "-t", id+":", "#{window_id}")
			second := s.must("-u", "new-window", "-d", "-P", "-F", "#{window_id}", "-t", id+":", "-n", windowName, "-c", path, "--", "/bin/cat")
			// Fixture only: a native window may have another link in its parent session.
			s.must("link-window", "-d", "-s", id+":"+first, "-t", id+":3")
			identity, err := model.EncodeSSHIdentity("dev", name)
			require.NoError(t, err)
			sh := sshWindowPrivateTransport(t, s, s.socket)
			snapshot := func() string {
				return s.must("list-sessions", "-F", "#{session_id}:#{session_name}:#{session_windows}:#{window_id}") + "\n" +
					s.must("list-panes", "-a", "-F", "#{session_id}:#{window_id}:#{pane_id}:#{window_active}:#{pane_active}") + "\n" +
					s.must("list-clients", "-F", "#{client_name}:#{session_id}")
			}
			before := snapshot()
			fingerprint := s.must("display-message", "-p", "-t", id+":", "#{pid}:#{start_time}:#{socket_path}")

			windows, err := tmux.ListSSHWindows(t.Context(), config, sh, identity)

			require.NoError(t, err)
			require.Equal(t, []model.SSHWindow{
				{Name: storedWindowName, Path: path, Index: 0, Active: true, Src: "ssh", Session: identity, SessionID: id, ID: first, ServerFingerprint: fingerprint},
				{Name: storedWindowName, Path: path, Index: 1, Active: false, Src: "ssh", Session: identity, SessionID: id, ID: second, ServerFingerprint: fingerprint},
				{Name: storedWindowName, Path: path, Index: 3, Active: false, Src: "ssh", Session: identity, SessionID: id, ID: first, ServerFingerprint: fingerprint},
			}, windows)
			require.Equal(t, before, snapshot(), "no new session/window/pane, selection or attached client")
			_, statErr := os.Stat(filepath.Join(s.dir, "MUST_NOT_RUN"))
			require.True(t, os.IsNotExist(statErr), "literal names cannot run shell substitutions")
			missing, err := tmux.ListSSHWindows(t.Context(), config, sh, "ssh/dev/missing")
			require.ErrorContains(t, err, "not found")
			require.Nil(t, missing)
			require.Equal(t, before, snapshot())
		})
	}
}

func TestSSHWindowDiscoveryNeverStartsAbsentServer(t *testing.T) {
	s := newInventoryPrivateShell(t)
	absent := filepath.Join(s.dir, "absent")
	sh := sshWindowPrivateTransport(t, s, absent)
	windows, err := tmux.ListSSHWindows(t.Context(), model.Config{SSH: model.SSHConfig{Hosts: []string{"dev"}}}, sh, "ssh/dev/work")
	require.Error(t, err)
	require.Nil(t, windows)
	_, statErr := os.Stat(absent)
	require.True(t, os.IsNotExist(statErr), "-N must not create a server/socket")
	require.Equal(t, "unrelated", s.must("display-message", "-p", "-t", "$0:", "#{session_name}"))
}

func TestSSHWindowDiscoveryDocumentsCustomHookEffects(t *testing.T) {
	s := newInventoryPrivateShell(t)
	s.must("set-hook", "-t", "$0", "after-list-windows", "set-option -t '$0' @ticket58-discovery-hook fired")
	sh := sshWindowPrivateTransport(t, s, s.socket)
	windows, err := tmux.ListSSHWindows(t.Context(), model.Config{SSH: model.SSHConfig{Hosts: []string{"dev"}}}, sh, "ssh/dev/unrelated")
	require.NoError(t, err)
	require.Len(t, windows, 1)
	require.Equal(t, "fired", s.must("show-options", "-v", "-t", "$0", "@ticket58-discovery-hook"))
	require.Equal(t, "$0:@0:%0", strings.TrimSpace(s.must("display-message", "-p", "-t", "$0:", "#{session_id}:#{window_id}:#{pane_id}")))
}
