//go:build unix

package tmux_test

import (
	"testing"

	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/require"
)

func TestTicket42NativeEmptyInventoryDiagnostics(t *testing.T) {
	s := newInventoryPrivateShell(t)
	s.scope = ""
	s.must("set-option", "-g", "exit-empty", "off")
	s.must("kill-session", "-t", "$0:")
	tm := tmux.NewTmux(nil, s, s.binary)
	for _, state := range []string{"running empty server", "missing server"} {
		t.Run(state, func(t *testing.T) {
			sessions, err := tm.ListSessions()
			require.NoError(t, err)
			require.Empty(t, sessions)
			panes, err := tm.ListTmuxPanes()
			require.NoError(t, err)
			require.Empty(t, panes)
		})
		if state == "running empty server" {
			s.must("kill-server")
		}
	}
}
