package tmux

import (
	"errors"
	"fmt"
	"testing"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/oswrap"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCurrentSessionID(t *testing.T) {
	tests := []struct {
		name     string
		tmuxEnv  string
		expected string
	}{
		{"popup and pane alike carry the session id", "/private/tmp/tmux-501/default,3746,31", "$31"},
		{"outside tmux", "", ""},
		{"truncated by a wrapper", "/private/tmp/tmux-501/default,3746", ""},
		{"empty session field", "/private/tmp/tmux-501/default,3746,", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, currentSessionID(tt.tmuxEnv))
		})
	}
}

// clientLines renders what `tmux list-clients -F clientFormat` would print.
func clientLines(lines ...string) []string {
	return append(lines, "")
}

func TestResolveClient(t *testing.T) {
	const (
		onSession1   = "/dev/ttys001::/dev/ttys001::$1::100"
		onSession2   = "/dev/ttys002::/dev/ttys002::$2::200"
		alsoSession1 = "/dev/ttys003::/dev/ttys003::$1::300"
	)

	tests := []struct {
		name       string
		seshClient string
		tmuxPane   string
		tmuxEnv    string
		clients    []string
		clientsErr error
		expected   string
	}{
		{
			name:       "SESH_CLIENT wins outright",
			seshClient: "/dev/ttys009",
			tmuxPane:   "%3",
			tmuxEnv:    "/tmp/default,3746,1",
			clients:    clientLines(onSession1),
			expected:   "/dev/ttys009",
		},
		{
			// From a pane tmux resolves the client from $TMUX_PANE exactly.
			// Guessing over the top of that is how sesh ended up moving a
			// terminal the user wasn't looking at.
			name:     "defers to tmux when the caller has a pane",
			tmuxPane: "%3",
			tmuxEnv:  "/tmp/default,3746,1",
			clients:  clientLines(onSession1, onSession2),
			expected: "",
		},
		{
			// The popup case: tmux would guess ttys002 as most recently
			// active server-wide, but the popup was launched from $1.
			name:     "prefers the client attached to the calling session",
			tmuxEnv:  "/tmp/default,3746,1",
			clients:  clientLines(onSession1, onSession2),
			expected: "/dev/ttys001",
		},
		{
			name:     "picks the most recently active of several on that session",
			tmuxEnv:  "/tmp/default,3746,1",
			clients:  clientLines(onSession1, alsoSession1),
			expected: "/dev/ttys003",
		},
		{
			name:     "falls back to most recently active when the session has no client",
			tmuxEnv:  "/tmp/default,3746,7",
			clients:  clientLines(onSession1, onSession2),
			expected: "/dev/ttys002",
		},
		{
			name:     "falls back to most recently active outside tmux",
			tmuxEnv:  "",
			clients:  clientLines(onSession1, onSession2),
			expected: "/dev/ttys002",
		},
		{
			// A GUI launcher can inherit a stale $TMUX_PANE from whatever
			// started it. Outside tmux there is no caller for tmux to resolve
			// from, so the pane must not short-circuit resolution.
			name:     "ignores a stale pane when outside tmux",
			tmuxPane: "%3",
			tmuxEnv:  "",
			clients:  clientLines(onSession1, onSession2),
			expected: "/dev/ttys002",
		},
		{
			name:     "no clients attached",
			tmuxEnv:  "/tmp/default,3746,1",
			clients:  clientLines(),
			expected: "",
		},
		{
			name:       "list-clients failed",
			tmuxEnv:    "/tmp/default,3746,1",
			clientsErr: errors.New("no server running"),
			expected:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockOs := oswrap.NewMockOs(t)
			mockShell := shell.NewMockShell(t)
			mockOs.EXPECT().Getenv("SESH_CLIENT").Return(tt.seshClient)
			if tt.seshClient == "" {
				mockOs.EXPECT().Getenv("TMUX").Return(tt.tmuxEnv)
				mockOs.EXPECT().Getenv("TMUX_PANE").Return(tt.tmuxPane)
			}
			// A pane only short-circuits resolution from inside tmux.
			if tt.seshClient == "" && !(tt.tmuxPane != "" && tt.tmuxEnv != "") {
				mockShell.EXPECT().ListCmd("tmux", "list-clients", "-F", clientFormat).
					Return(tt.clients, tt.clientsErr)
			}
			tm := NewTmux(mockOs, mockShell, "")
			assert.Equal(t, tt.expected, tm.ResolveClient())
		})
	}
}

func TestSwitchOrAttachNamesTheResolvedClient(t *testing.T) {
	mockOs := oswrap.NewMockOs(t)
	mockShell := shell.NewMockShell(t)
	mockOs.EXPECT().Getenv("SESH_CLIENT").Return("")
	mockOs.EXPECT().Getenv("TMUX_PANE").Return("")
	mockOs.EXPECT().Getenv("TMUX").Return("/tmp/default,3746,1")
	mockShell.EXPECT().ListCmd("tmux", "list-clients", "-F", clientFormat).
		Return([]string{
			"/dev/ttys001::/dev/ttys001::$1::100",
			"/dev/ttys002::/dev/ttys002::$2::200",
		}, nil)
	mockShell.EXPECT().Cmd("tmux", "switch-client", "-c", "/dev/ttys001", "-t", "dotfiles").
		Return("", nil)

	tm := NewTmux(mockOs, mockShell, "")
	response, err := tm.SwitchOrAttach("dotfiles", model.ConnectOpts{Switch: true})
	require.NoError(t, err)
	assert.Equal(t, "switching to tmux session: dotfiles", response)
}

func TestFocusedSessionPathFromPaneLeavesTargetToTmux(t *testing.T) {
	mockOs := oswrap.NewMockOs(t)
	mockShell := shell.NewMockShell(t)
	mockOs.EXPECT().Getenv("SESH_CLIENT").Return("")
	mockOs.EXPECT().Getenv("TMUX").Return("/tmp/default,3746,1")
	mockOs.EXPECT().Getenv("TMUX_PANE").Return("%3")
	mockShell.EXPECT().Cmd("tmux", "display-message", "-p", "#{session_id}").Return("$1", nil)
	mockShell.EXPECT().CmdCapture("tmux", "show-environment", "-t", "$1", "--", model.SSHBridgeIdentityEnv).Return("", nil)
	mockShell.EXPECT().Cmd("tmux", "display-message", "-p", "-t", "$1:", "#{session_path}").
		Return("/repo/.wk/409", nil)

	path, err := NewTmux(mockOs, mockShell, "").FocusedSessionPath()
	require.NoError(t, err)
	assert.Equal(t, "/repo/.wk/409", path)
}

func TestFocusedSessionPathOutsideTmuxTargetsResolvedClientSession(t *testing.T) {
	mockOs := oswrap.NewMockOs(t)
	mockShell := shell.NewMockShell(t)
	mockOs.EXPECT().Getenv("SESH_CLIENT").Return("")
	mockOs.EXPECT().Getenv("TMUX").Return("")
	mockOs.EXPECT().Getenv("TMUX_PANE").Return("")
	mockShell.EXPECT().ListCmd("tmux", "list-clients", "-F", clientFormat).
		Return([]string{
			"/dev/ttys001::/dev/ttys001::$1::100",
			"/dev/ttys002::/dev/ttys002::$2::200",
		}, nil)
	mockShell.EXPECT().Cmd("tmux", "display-message", "-p", "-t", "$2:", "#{session_id}").Return("$2", nil)
	mockShell.EXPECT().CmdCapture("tmux", "show-environment", "-t", "$2", "--", model.SSHBridgeIdentityEnv).Return("", nil)
	mockShell.EXPECT().Cmd("tmux", "display-message", "-p", "-t", "$2:", "#{session_path}").
		Return("/repo/.wk/409", nil)

	path, err := NewTmux(mockOs, mockShell, "").FocusedSessionPath()
	require.NoError(t, err)
	assert.Equal(t, "/repo/.wk/409", path)
}

func TestFocusedSessionPathRejectsBridgesAndOwnershipFailures(t *testing.T) {
	for _, inside := range []bool{true, false} {
		for _, failure := range []string{"bridge", "marker read", "missing ID", "ID-shaped name"} {
			t.Run(fmt.Sprintf("inside=%t/%s", inside, failure), func(t *testing.T) {
				mockOs := oswrap.NewMockOs(t)
				mockShell := shell.NewMockShell(t)
				mockOs.EXPECT().Getenv("SESH_CLIENT").Return("")
				if inside {
					mockOs.EXPECT().Getenv("TMUX").Return("/tmp/default,3746,7")
					mockOs.EXPECT().Getenv("TMUX_PANE").Return("%3")
				} else {
					mockOs.EXPECT().Getenv("TMUX").Return("")
					mockOs.EXPECT().Getenv("TMUX_PANE").Return("")
					mockShell.EXPECT().ListCmd("tmux", "list-clients", "-F", clientFormat).Return(clientLines(
						"other::/dev/ttys001::$1::100", "focused::/dev/ttys002::$7::200"), nil)
				}
				id := "$7"
				if failure == "missing ID" {
					id = ""
				}
				if failure == "ID-shaped name" {
					id = "$7suffix"
				}
				if inside {
					mockShell.EXPECT().Cmd("tmux", "display-message", "-p", "#{session_id}").Return(id, nil)
				} else {
					mockShell.EXPECT().Cmd("tmux", "display-message", "-p", "-t", "$7:", "#{session_id}").Return(id, nil)
				}
				if failure == "bridge" {
					mockShell.EXPECT().CmdCapture("tmux", "show-environment", "-t", "$7", "--", model.SSHBridgeIdentityEnv).
						Return(model.SSHBridgeIdentityEnv+"=ssh/host/work", nil)
				} else if failure == "marker read" {
					mockShell.EXPECT().CmdCapture("tmux", "show-environment", "-t", "$7", "--", model.SSHBridgeIdentityEnv).
						Return("", errors.New("server unavailable"))
				}
				path, err := NewTmux(mockOs, mockShell, "").FocusedSessionPath()
				require.Error(t, err)
				assert.Empty(t, path)
				if failure == "bridge" {
					assert.Contains(t, err.Error(), "focus a local session")
				}
			})
		}
	}
}

func TestFocusedSessionPathFailsClosedOnClientResolution(t *testing.T) {
	for _, failure := range []string{"listing", "selected client read", "disappeared", "missing session", "no clients"} {
		t.Run(failure, func(t *testing.T) {
			mOs := oswrap.NewMockOs(t)
			mShell := shell.NewMockShell(t)
			mOs.EXPECT().Getenv("SESH_CLIENT").Return("")
			mOs.EXPECT().Getenv("TMUX").Return("")
			// A stale pane outside tmux must not authorize an untargeted query.
			mOs.EXPECT().Getenv("TMUX_PANE").Return("%3")
			selected := clientLines("bridge::/dev/tty1::$7::200", "local::/dev/tty2::$1::100")
			first := mShell.EXPECT().ListCmd("tmux", "list-clients", "-F", clientFormat)
			if failure == "listing" {
				first.Return(nil, errors.New("list failed")).Once()
			} else if failure == "no clients" {
				first.Return(nil, nil).Once()
			} else {
				first.Return(selected, nil).Once()
				second := mShell.EXPECT().ListCmd("tmux", "list-clients", "-F", clientFormat)
				if failure == "selected client read" {
					second.Return(nil, errors.New("read failed")).Once()
				} else if failure == "missing session" {
					second.Return(clientLines("bridge::/dev/tty1::::200"), nil).Once()
				} else {
					second.Return(clientLines("local::/dev/tty2::$1::100"), nil).Once()
				}
			}
			path, err := NewTmux(mOs, mShell, "").FocusedSessionPath()
			require.Error(t, err)
			assert.Empty(t, path)
			for _, call := range mShell.Calls {
				assert.Equal(t, "ListCmd", call.Method, "failed resolution must not query any session ID, marker, or path")
			}
		})
	}
}
