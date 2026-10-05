package tmux

import (
	"errors"
	"testing"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/oswrap"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListClients(t *testing.T) {
	s := shell.NewMockShell(t)
	// ListCmd splits on newlines, so the trailing blank line comes through as
	// an empty entry and has to be dropped.
	s.EXPECT().ListCmd("tmux", "list-clients", "-F", clientFormat).
		Return([]string{
			"/dev/ttys001::/dev/ttys001::$1::1788188899",
			"/dev/ttys002::/dev/ttys002::$2::1788188999",
			"",
		}, nil)
	os := oswrap.NewMockOs(t)
	tm := NewTmux(os, s, "")
	clients, err := tm.ListClients()
	require.NoError(t, err)
	assert.Equal(t, []model.TmuxClient{
		{Name: "/dev/ttys001", TTY: "/dev/ttys001", SessionID: "$1", Activity: 1788188899},
		{Name: "/dev/ttys002", TTY: "/dev/ttys002", SessionID: "$2", Activity: 1788188999},
	}, clients)
}

// A client sesh cannot read is not a server with nothing attached. Reporting
// it as no clients is how a GUI launcher ended up creating a session, focusing
// the terminal, and never switching to it.
func TestListClientsRejectsUnreadableLines(t *testing.T) {
	s := shell.NewMockShell(t)
	s.EXPECT().ListCmd("tmux", "list-clients", "-F", clientFormat).
		Return([]string{"/dev/ttys001_/dev/ttys001_$1_1788188899", ""}, nil)
	os := oswrap.NewMockOs(t)
	tm := NewTmux(os, s, "")
	_, err := tm.ListClients()
	require.ErrorContains(t, err, "unreadable format")
}

// No clients at all is a real, readable answer: the server is up with nothing
// attached.
func TestListClientsWithNoClientsAttached(t *testing.T) {
	s := shell.NewMockShell(t)
	s.EXPECT().ListCmd("tmux", "list-clients", "-F", clientFormat).
		Return([]string{""}, nil)
	os := oswrap.NewMockOs(t)
	tm := NewTmux(os, s, "")
	clients, err := tm.ListClients()
	require.NoError(t, err)
	assert.Empty(t, clients)
}

func TestSwitchClientTarget(t *testing.T) {
	s := shell.NewMockShell(t)
	s.EXPECT().Cmd("tmux", "switch-client", "-c", "/dev/ttys001", "-t", "sesh").
		Return("", nil)
	os := oswrap.NewMockOs(t)
	tm := NewTmux(os, s, "")
	_, err := tm.SwitchClientTarget("/dev/ttys001", "sesh")
	require.NoError(t, err)
}

func TestNewSessionWithCommand(t *testing.T) {
	const command = `exec worker 'argument with spaces' "café"`
	environment := []string{"MODE=fast", `LABEL=a=b 'quoted' $(); café`, "EMPTY="}
	s := shell.NewMockShell(t)
	s.EXPECT().Cmd("tmux", "new-session", "-d", "-s", "worker", "-c", "/work space",
		"-e", "MODE=fast", "-e", `LABEL=a=b 'quoted' $(); café`, "-e", "EMPTY=", "--", command).
		Return("created", nil).Once()
	tm := NewTmux(nil, s, "")

	output, err := tm.NewSessionWithCommand(model.TmuxSessionOpts{
		Name: "worker", StartDir: "/work space", Command: command, Environment: environment,
	})

	require.NoError(t, err)
	assert.Equal(t, "created", output)
	assert.Equal(t, []string{"MODE=fast", `LABEL=a=b 'quoted' $(); café`, "EMPTY="}, environment)
}

func TestNewSessionWithCommandWithoutDirectoryOrEnvironment(t *testing.T) {
	const command = `-worker 'quoted argument'; echo "$value"`
	s := shell.NewMockShell(t)
	s.EXPECT().Cmd("/opt/bin/tmux", "new-session", "-d", "-s", "worker", "--", command).
		Return("", nil).Once()
	tm := NewTmux(nil, s, "/opt/bin/tmux")

	_, err := tm.NewSessionWithCommand(model.TmuxSessionOpts{Name: "worker", Command: command})

	require.NoError(t, err)
}

func TestNewSessionWithCommandEmptyStartsDefaultShell(t *testing.T) {
	s := shell.NewMockShell(t)
	s.EXPECT().Cmd("tmux", "new-session", "-d", "-s", "worker", "-e", "MODE=fast").
		Return("", nil).Once()
	tm := NewTmux(nil, s, "")

	_, err := tm.NewSessionWithCommand(model.TmuxSessionOpts{Name: "worker", Environment: []string{"MODE=fast"}})

	require.NoError(t, err)
}

func TestNewSessionWithCommandPropagatesCreationFailure(t *testing.T) {
	wantErr := errors.New("session creation failed")
	s := shell.NewMockShell(t)
	s.EXPECT().Cmd("tmux", "new-session", "-d", "-s", "worker", "--", "exec worker").
		Return("diagnostic", wantErr).Once()
	tm := NewTmux(nil, s, "")

	output, err := tm.NewSessionWithCommand(model.TmuxSessionOpts{Name: "worker", Command: "exec worker"})

	require.ErrorIs(t, err, wantErr)
	assert.Equal(t, "diagnostic", output)
}
