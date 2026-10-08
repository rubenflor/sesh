package tmux

import (
	"testing"

	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectWindow(t *testing.T) {
	for _, test := range []struct{ name, configured, executable string }{
		{"default", "", "tmux"},
		{"custom command", "custom-tmux", "custom-tmux"},
		{"wrapper path", "/tmp/private wrapper", "/tmp/private wrapper"},
		{"homebrew path", "/opt/homebrew/bin/tmux", "/opt/homebrew/bin/tmux"},
		{"local path", "/usr/local/bin/tmux", "/usr/local/bin/tmux"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, commandErr := range []error{nil, assert.AnError} {
				s := shell.NewMockShell(t)
				s.EXPECT().Cmd(test.executable, "select-window", "-t", "$1:@7").Return("operation output", commandErr).Once()
				tm := NewTmux(nil, s, test.configured)
				result, err := tm.SelectWindow("$1:@7")
				require.ErrorIs(t, err, commandErr)
				require.Equal(t, "operation output", result)
			}
		})
	}
}
