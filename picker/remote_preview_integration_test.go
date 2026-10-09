//go:build unix

package picker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestSSHRemotePreviewNativePrivateTmux(t *testing.T) {
	binary, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	t.Setenv("LC_ALL", "C")
	fixture := newDeletionPrivateShell(t, binary)
	run := func(args ...string) string {
		t.Helper()
		output, err := fixture.run(args...)
		require.NoError(t, err)
		return output
	}
	paneCommand := func(marker string) string {
		return "printf '%s\\n' " + shell.Quote(marker) + "; exec sleep 60"
	}
	create := func(name, marker string) (string, error) {
		// new-session expands format hashes and consumes a final semicolon escape.
		argument := strings.ReplaceAll(name, "#", "##")
		if strings.HasSuffix(argument, ";") {
			argument = strings.TrimSuffix(argument, ";") + `\;`
		}
		output, err := fixture.run("-u", "new-session", "-d", "-x", "120", "-y", "30",
			"-s", argument, "-P", "-F", "#{pid} #{session_id}", "--", "/bin/sh", "-c", paneCommand(marker))
		if err != nil {
			return "", err
		}
		fields := strings.Fields(output)
		require.Len(t, fields, 2)
		if len(fixture.pids) == 0 {
			fixture.recordPID(t, fields[0])
		}
		return fields[1], nil
	}
	seed, err := create("seed", "SEED")
	require.NoError(t, err)
	require.Equal(t, "$0", seed)
	work, err := create("work", "OTHER_SESSION_WORK")
	require.NoError(t, err)
	require.Equal(t, "$1", work)
	literalID, err := create("$1", "SELECTED_LITERAL_DOLLAR")
	require.NoError(t, err)
	require.Equal(t, "$2", literalID)
	colon, err := create("colon", "OTHER_SESSION_COLON")
	require.NoError(t, err)
	for _, name := range []string{"name", "name:"} {
		run("new-window", "-d", "-t", colon+":", "-n", name, "--", "/bin/sh", "-c", paneCommand("OTHER_COLON_WINDOW"))
	}

	bin := filepath.Join(fixture.dir, "bin")
	require.NoError(t, os.Mkdir(bin, 0700))
	// Only transport is mocked: every tmux command reaches the private server.
	isolated := shell.Quote(binary) + " -S " + shell.Quote(fixture.socket) + " -f /dev/null "
	require.NoError(t, os.WriteFile(filepath.Join(bin, "tmux"), []byte("#!/bin/sh\n"+
		"if [ \"$1\" = capture-pane ] && [ \"$4\" = \"${SESH_PREVIEW_REMOVE_ID}:\" ]; then\n"+
		" "+isolated+"kill-session -t \"$SESH_PREVIEW_REMOVE_ID\" || exit $?\nfi\n"+
		"exec "+isolated+"\"$@\"\n"), 0700))
	preview := func(t *testing.T, name string) (string, error) {
		t.Helper()
		transport := shell.NewMockShell(t)
		transport.EXPECT().CmdCaptureRaw(mock.Anything, "ssh", sshPreviewBytes, sshPreviewStderr,
			"-T", "-n", "-o", "BatchMode=yes", "--", "fixture", mock.Anything).
			RunAndReturn(func(ctx context.Context, _ string, stdoutLimit, stderrLimit int, args ...string) (execwrap.CommandResult, error) {
				return shell.NewShell(execwrap.NewExec(), nil).CmdCaptureRaw(ctx, "/usr/bin/env", stdoutLimit, stderrLimit,
					"-u", "TMUX", "-u", "TMUX_PANE", "-u", "SESH_CLIENT",
					"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "/bin/sh", "-c", args[6])
			}).Once()
		identity, err := model.EncodeSSHIdentity("fixture", name)
		require.NoError(t, err)
		p := &RealPicker{config: model.Config{SSH: model.SSHConfig{Hosts: []string{"fixture"}}},
			shell: transport, tmux: tmux.NewMockTmux(t)}
		return p.previewSession(t.Context(), model.SeshSession{Src: "ssh", Name: identity})
	}
	state := func() string {
		return run("list-panes", "-a", "-F", "#{session_id}|#{window_id}|#{pane_id}|#{window_active}|#{pane_active}") +
			run("list-clients", "-F", "#{client_name}")
	}

	for _, test := range []struct {
		label, name, id, marker string
		optional                bool
	}{
		{label: "native ID collision", name: "$1", id: literalID, marker: "SELECTED_LITERAL_DOLLAR"},
		{label: "colon collision", name: "colon:name"},
		{label: "leading dollar", name: "$foo"},
		{label: "active window and pane", name: "deploy"},
		{label: "spaces", name: "two words"},
		{label: "shell characters", name: "café 'quotes' \"double\" $(echo unsafe) `echo unsafe` *[ab]?;"},
		{label: "literal backslash", name: `line\nbreak`},
		{label: "format and field delimiters", name: `::|,#{session_id}#}"`},
		{label: "line break", name: "line\nbreak", optional: true},
		{label: "tab and controls", name: "tab\tcontrol\x01\x7f", optional: true},
	} {
		t.Run(test.label, func(t *testing.T) {
			id, marker := test.id, test.marker
			if id == "" {
				marker = "SELECTED_" + test.label
				var err error
				id, err = create(test.name, marker)
				if test.optional && err != nil && strings.Contains(err.Error(), "invalid session name") {
					t.Skipf("installed tmux does not support this name: %v", err)
				}
				require.NoError(t, err)
			}
			if test.name == "deploy" {
				window := run("new-window", "-d", "-t", id+":", "-P", "-F", "#{window_id}",
					"--", "/bin/sh", "-c", paneCommand("WRONG_ACTIVE_WINDOW_PANE"))
				pane := run("split-window", "-d", "-t", id+":"+window, "-P", "-F", "#{pane_id}",
					"--", "/bin/sh", "-c", paneCommand(marker))
				run("select-window", "-t", id+":"+window)
				run("select-pane", "-t", pane)
			}
			require.Eventually(t, func() bool {
				return strings.Contains(run("capture-pane", "-p", "-t", id+":"), marker)
			}, 2*time.Second, 10*time.Millisecond)
			before := state()
			output, err := preview(t, test.name)
			require.NoError(t, err, "selected name %q; name read by native ID: %q", test.name,
				run("-u", "display-message", "-p", "-t", id+":", "#{session_name}x"))
			require.Contains(t, output, marker)
			require.NotContains(t, output, "OTHER_")
			require.NotContains(t, output, "WRONG_")
			require.Equal(t, before, state(), "preview must preserve focus, topology and clients")
		})
	}

	for _, name := range []string{"wor", "colon:missing", "$99999"} {
		t.Run("missing "+name, func(t *testing.T) {
			before := state()
			output, err := preview(t, name)
			require.Error(t, err)
			require.Empty(t, output)
			require.Equal(t, before, state())
		})
	}
	t.Run("disappeared", func(t *testing.T) {
		run("kill-session", "-t", literalID)
		before := state()
		output, err := preview(t, "$1")
		require.Error(t, err)
		require.Empty(t, output)
		require.Equal(t, before, state())
	})
	t.Run("disappeared after lookup", func(t *testing.T) {
		id, err := create("disappearing", "SELECTED_DISAPPEARING")
		require.NoError(t, err)
		other, err := create(id, "OTHER_NATIVE_ID_NAMESAKE")
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			return strings.Contains(run("capture-pane", "-p", "-t", other+":"), "OTHER_NATIVE_ID_NAMESAKE")
		}, 2*time.Second, 10*time.Millisecond)
		otherState := run("display-message", "-p", "-t", other+":", "#{session_id}|#{pane_id}|#{pane_pid}")
		t.Setenv("SESH_PREVIEW_REMOVE_ID", id)
		output, err := preview(t, "disappearing")
		require.Error(t, err, "a missing native ID must never fall back to its literal namesake")
		require.Empty(t, output)
		require.Equal(t, otherState, run("display-message", "-p", "-t", other+":", "#{session_id}|#{pane_id}|#{pane_pid}"))
		require.Contains(t, run("capture-pane", "-p", "-t", other+":"), "OTHER_NATIVE_ID_NAMESAKE")
	})
}
