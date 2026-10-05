package connector

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSSHAttachCommand(t *testing.T) {
	command, err := sshAttachCommand("ssh/myinventa/0")

	require.NoError(t, err)
	assert.Equal(t, `exec ssh -tt -- 'myinventa' 'tmux attach-session -t '\''=0'\'''`, command)
}

func TestSSHAttachCommandTrailingTmuxSeparator(t *testing.T) {
	command, err := sshAttachCommand("ssh/myinventa/work%3B")

	require.NoError(t, err)
	assert.Equal(t, `exec ssh -tt -- 'myinventa' 'tmux attach-session -t '\''=work\;'\'''`, command)
}

func TestSSHAttachCommandDecodesComponentsOnce(t *testing.T) {
	command, err := sshAttachCommand("ssh/my%2Fhost/session%252F")

	require.NoError(t, err)
	assert.Equal(t, `exec ssh -tt -- 'my/host' 'tmux attach-session -t '\''=session%2F'\'''`, command)
}

func TestSSHAttachCommandRejectsInvalidIdentity(t *testing.T) {
	for _, identity := range []string{
		"tmux/host/0", "ssh/host/session/part", "ssh/host/%GG", "ssh/host/a%2fb", "ssh/host/%7E",
	} {
		t.Run(identity, func(t *testing.T) {
			command, err := sshAttachCommand(identity)
			require.Error(t, err)
			assert.Empty(t, command)
		})
	}
}

func TestSSHAttachCommandShellBoundaries(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX executable scripts")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("requires a POSIX shell")
	}
	binDir := t.TempDir()
	// Fake ssh records the local argv, then runs the remote command through a
	// second real shell. No network connection or actual tmux server is used.
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "ssh"), []byte(`#!/bin/sh
printf '%s\0' "$@" > "$SSH_CAPTURE"
[ "$#" -eq 4 ] || exit 1
exec /bin/sh -c "$4"
`), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "tmux"), []byte(`#!/bin/sh
printf '%s\0' "$@" > "$TMUX_CAPTURE"
`), 0o700))
	tests := []struct {
		name, host, session, target string
	}{
		{"plain exact name", "host", "work", "=work"},
		{"leading hyphens", "-oProxyCommand=evil", "-target", "=-target"},
		{"slashes", "my/host", "nutiliti/2345", "=nutiliti/2345"},
		{"percent", "host%alias", "literal%2F", "=literal%2F"},
		{"Unicode", "höst", "café 東京", "=café 東京"},
		{"spaces", "host alias", "two words", "=two words"},
		{"quotes", `host'"alias`, `it's "ready"`, `=it's "ready"`},
		{"substitution", `$(touch "$SSH_INJECTED")`, `$(touch "$TMUX_INJECTED")`, `=$(touch "$TMUX_INJECTED")`},
		{"backticks", "`touch \"$SSH_INJECTED\"`", "`touch \"$TMUX_INJECTED\"`", "=`touch \"$TMUX_INJECTED\"`"},
		{"semicolons", `host; touch "$SSH_INJECTED"; #`, `work; touch "$TMUX_INJECTED"; #`, `=work; touch "$TMUX_INJECTED"; #`},
		{"quote breakout", `host'; touch "$SSH_INJECTED"; #`, `work'; touch "$TMUX_INJECTED"; #`, `=work'; touch "$TMUX_INJECTED"; #`},
		{"newlines", "host\ntouch \"$SSH_INJECTED\"", "work\ntouch \"$TMUX_INJECTED\"", "=work\ntouch \"$TMUX_INJECTED\""},
		{"glob name", "host", "work*[ab]?", "=work*[ab]?"},
		{"session ID shaped name", "host", "$1", "=$1"},
		{"trailing semicolon", "host", "work;", `=work\;`},
		{"repeated trailing semicolons", "host", "work;;", `=work;\;`},
		{"backslash before semicolon", "host", `work\;`, `=work\\;`},
		{"only semicolon", "host", ";", `=\;`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identity, err := model.EncodeSSHIdentity(tt.host, tt.session)
			require.NoError(t, err)
			command, err := sshAttachCommand(identity)
			require.NoError(t, err)
			dir := t.TempDir()
			sshCapture, tmuxCapture := filepath.Join(dir, "ssh-argv"), filepath.Join(dir, "tmux-argv")
			sshInjected, tmuxInjected := filepath.Join(dir, "ssh-injected"), filepath.Join(dir, "tmux-injected")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			t.Cleanup(cancel)
			process := exec.CommandContext(ctx, sh, "-c", command)
			process.Env = append(os.Environ(),
				"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"SSH_CAPTURE="+sshCapture, "TMUX_CAPTURE="+tmuxCapture,
				"SSH_INJECTED="+sshInjected, "TMUX_INJECTED="+tmuxInjected)
			output, err := process.CombinedOutput()
			require.NoError(t, err, "%s", output)
			assert.Empty(t, output)
			sshArgs := readCapturedArgs(t, sshCapture)
			require.Len(t, sshArgs, 4)
			assert.Equal(t, []string{"-tt", "--", tt.host}, sshArgs[:3])
			assert.Equal(t, []string{"attach-session", "-t", tt.target}, readCapturedArgs(t, tmuxCapture))
			for _, sentinel := range []string{sshInjected, tmuxInjected} {
				_, err := os.Stat(sentinel)
				assert.ErrorIs(t, err, os.ErrNotExist, "no injected command may create %s", sentinel)
			}
		})
	}
}

func readCapturedArgs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
}
