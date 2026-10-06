package tmux

import (
	"errors"
	"testing"

	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKillSession(t *testing.T) {
	t.Run("calls tmux kill-session with a session-scoped target", func(t *testing.T) {
		mockShell := &shell.MockShell{}
		tmux := &RealTmux{shell: mockShell, bin: "tmux"}
		mockShell.EXPECT().
			Cmd("tmux", "kill-session", "-t", "my-project:").
			Return("", nil)

		result, err := tmux.KillSession("my-project")

		assert.Nil(t, err)
		assert.Equal(t, "", result)
	})
}

func TestKillSessionInGeneration(t *testing.T) {
	const generation = "101:1700000000:/tmp/socket,#[x]#{x}#}::fixture"
	const condition = "#{==:#{pid}:#{start_time}:#{socket_path},#{l:101:1700000000:/tmp/socket#,##[x]##{x#}###}::fixture}}"
	for _, fixture := range []struct {
		name      string
		output    string
		err       error
		wantError bool
	}{
		{name: "matching server"},
		{name: "replaced server", output: "sesh: tmux server generation changed", wantError: true},
		{name: "unreadable server", err: errors.New("socket unavailable"), wantError: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			s := shell.NewMockShell(t)
			s.EXPECT().CmdCapture("custom-tmux", "if-shell", "-F", "-t", "$1:", condition,
				"kill-session -t '$1:'", "display-message -p 'sesh: tmux server generation changed'").
				Return(fixture.output, fixture.err).Once()
			_, err := NewTmux(nil, s, "custom-tmux").KillSessionInGeneration("$1", generation)
			if fixture.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestKillSessionInGenerationRejectsUnknownIdentity(t *testing.T) {
	for _, fixture := range []struct{ id, generation string }{
		{"", "101:1700000000:/tmp/socket"},
		{"literal-name", "101:1700000000:/tmp/socket"},
		{"$01", "101:1700000000:/tmp/socket"},
		{"$1", ""},
		{"$1", "101::/tmp/socket"},
		{"$1", "101:0:/tmp/socket"},
		{"$1", "0101:1700000000:/tmp/socket"},
		{"$1", "101:1700000000:"},
		{"$1", "101:1700000000:#{socket_path}"},
		{"$1", "101:1700000000:/tmp/socket\nother"},
	} {
		t.Run(fixture.id+"@"+fixture.generation, func(t *testing.T) {
			s := shell.NewMockShell(t)
			_, err := NewTmux(nil, s, "custom-tmux").KillSessionInGeneration(fixture.id, fixture.generation)
			require.Error(t, err)
		})
	}
}
