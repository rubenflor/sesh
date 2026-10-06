package connector

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/stretchr/testify/require"
)

// Execute the actual SSH discovery command through a fake transport and a real
// private tmux server. Attach is replaced only at the terminal boundary with
// has-session, which verifies that the decoded exact target exists.
func TestSSHInventoryPrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	// Unix socket paths must be short on macOS.
	dir, err := os.MkdirTemp("", "sesh-inv-")
	require.NoError(t, err)
	socket := filepath.Join(dir, "socket")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, binary, append([]string{"-u", "-S", socket, "-f", "/dev/null"}, args...)...).CombinedOutput()
		require.NoError(t, err, "%s", out)
		return string(out)
	}
	t.Cleanup(func() {
		_ = exec.Command(binary, "-S", socket, "kill-server").Run()
		_ = os.RemoveAll(dir)
	})
	bin := t.TempDir()
	capture := filepath.Join(dir, "attach-args")
	require.NoError(t, os.WriteFile(filepath.Join(bin, "ssh"), []byte(`#!/bin/sh
case "$1" in
 -T) [ "$#" -eq 7 ] || exit 9; exec /bin/sh -c "$7" ;;
 -tt) [ "$#" -eq 4 ] || exit 9; exec /bin/sh -c "$4" ;;
 *) exit 9 ;;
esac
`), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\n"+
		"if [ \"$1\" = attach-session ]; then\n"+
		" printf '%s\\0' \"$@\" > "+shell.Quote(capture)+"\n"+
		" exec "+shell.Quote(binary)+" -S \"$SESH_TEST_SOCKET\" has-session -t \"$3\"\nfi\n"+
		"exec "+shell.Quote(binary)+" -S \"$SESH_TEST_SOCKET\" \"$@\"\n"), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SESH_TEST_SOCKET", socket)
	t.Setenv("LC_ALL", "C") // UTF-8 inventory must not depend on the SSH client's locale.
	config := model.Config{SSH: model.SSHConfig{Hosts: []string{"fixture"}}}
	inventory := lister.NewLister(config, nil, nil, nil, nil, nil, nil, execwrap.NewExec())
	list := func() model.SeshSessions {
		t.Helper()
		sessions, err := inventory.List(lister.ListOptions{Context: ctx, SSH: true})
		require.NoError(t, err)
		return sessions
	}
	// An absent server becomes empty without sourcing a config or creating a
	// session. An existing empty server also succeeds and retains its options.
	require.Empty(t, list().OrderedIndex)
	run("start-server", ";", "set-option", "-g", "exit-empty", "off")
	require.Empty(t, list().OrderedIndex)
	require.Equal(t, "0\n", run("display-message", "-p", "#{server_sessions}"))
	require.Equal(t, "off\n", run("show-options", "-s", "-v", "exit-empty"))

	names := []string{"line\nbreak", `line\nbreak`, `line\\nbreak`, `slash\`, `literal\033`,
		"escape\x1b", "tab\treturn\r", "control\x01\x7f", "café/100% 'quotes' $()`;"}
	byID := make(map[string]string, len(names))
	for _, name := range names {
		argument := name
		if strings.HasSuffix(argument, ";") {
			argument = strings.TrimSuffix(argument, ";") + `\;`
		}
		id := strings.TrimSpace(run("new-session", "-d", "-P", "-F", "#{session_id}", "-s", argument, "sleep 60"))
		byID[id] = name
	}
	// Read order independently via server-assigned IDs, never by parsing names.
	ids := strings.Fields(run("list-sessions", "-F", "#{session_id}"))
	sessions := list()
	require.Len(t, sessions.Directory, len(names), "escaped-looking names must remain distinct")
	require.Len(t, sessions.OrderedIndex, len(ids))
	resolver := sshResolver(t, config)
	for i, id := range ids {
		row := sessions.Directory[sessions.OrderedIndex[i]]
		host, name, err := model.ParseSSHIdentity(row.Name)
		require.NoError(t, err)
		require.Equal(t, "fixture", host)
		require.Equal(t, byID[id], name, "inventory preserves original bytes and tmux order")
		connection, err := resolver.Resolve(row.Name)
		require.NoError(t, err)
		require.Equal(t, row.Name, connection.Session.Name)
		command, err := sshAttachCommand(connection.Session.Name)
		require.NoError(t, err)
		out, err := exec.CommandContext(ctx, "/bin/sh", "-c", command).CombinedOutput()
		require.NoError(t, err, "%s", out)
		storedName := strings.TrimSuffix(run("display-message", "-p", "-t", id, "#{session_name}"), "\n")
		target := "=" + storedName
		if strings.HasSuffix(target, ";") {
			target = strings.TrimSuffix(target, ";") + `\;`
		}
		require.Equal(t, []string{"attach-session", "-t", target}, readCapturedArgs(t, capture))
	}
	// This transport error also exits 1, but must never become empty success.
	badSocket := filepath.Join(dir, "not-a-socket")
	require.NoError(t, os.WriteFile(badSocket, []byte("preserve"), 0600))
	t.Setenv("SESH_TEST_SOCKET", badSocket)
	_, err = inventory.List(lister.ListOptions{Context: ctx, SSH: true})
	require.ErrorContains(t, err, "all configured SSH hosts failed")
	result, err := execwrap.NewExec().RunContext(ctx, binary, "-S", badSocket, "list-sessions")
	require.Error(t, err)
	require.Equal(t, 1, result.ExitCode)
	data, err := os.ReadFile(badSocket)
	require.NoError(t, err)
	require.Equal(t, "preserve", string(data))
}
