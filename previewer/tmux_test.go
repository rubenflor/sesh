package previewer

import (
	"testing"

	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTmuxPreviewKeepsCapturedIdentity(t *testing.T) {
	const generation = "101:1700000000:/tmp/tmux-fixture"
	m := tmux.NewMockTmux(t)
	li := lister.NewMockLister(t)
	li.EXPECT().FindTmuxSession("$0").Return(model.SeshSession{Name: "$0", TmuxSessionID: "$1", TmuxServerGeneration: generation}, true).Once()
	m.EXPECT().ListSessions().Return([]*model.TmuxSession{
		{ID: "$0", Name: "bridge", ServerGeneration: generation, SSHBridgeIdentity: "ssh/host/work"},
		{ID: "$1", Name: "renamed", ServerGeneration: generation},
		{ID: "$2", Name: "$0", ServerGeneration: generation},
	}, nil).Once()
	m.EXPECT().CapturePane("$1", generation).Return("original output", nil).Once()

	output, err := NewTmuxStrategy(li, m).Execute("$0")

	require.NoError(t, err)
	require.Equal(t, "original output", output)
}

func TestTmuxPreviewFailsClosed(t *testing.T) {
	const generation = "101:1700000000:/tmp/tmux-fixture"
	for _, invalid := range []string{"missing ID", "missing generation", "disappeared", "restarted", "marked", "lookup error", "capture error"} {
		t.Run(invalid, func(t *testing.T) {
			m := tmux.NewMockTmux(t)
			li := lister.NewMockLister(t)
			selected := model.SeshSession{Name: "$0", TmuxSessionID: "$1", TmuxServerGeneration: generation}
			live := &model.TmuxSession{ID: "$1", Name: "renamed", ServerGeneration: generation}
			switch invalid {
			case "missing ID":
				selected.TmuxSessionID = ""
			case "missing generation":
				selected.TmuxServerGeneration = ""
			case "disappeared":
				live.ID = "$3"
			case "restarted":
				live.ServerGeneration = "102:1700000001:/tmp/tmux-fixture"
			case "marked":
				live.SSHBridgeIdentity = "ssh/host/work"
			}
			li.EXPECT().FindTmuxSession("$0").Return(selected, true).Once()
			if selected.TmuxSessionID != "" && selected.TmuxServerGeneration != "" {
				var lookupErr error
				if invalid == "lookup error" {
					lookupErr = assert.AnError
				}
				m.EXPECT().ListSessions().Return([]*model.TmuxSession{live, {ID: "$2", Name: "$0", ServerGeneration: generation}}, lookupErr).Once()
			}
			if invalid == "capture error" {
				m.EXPECT().CapturePane("$1", generation).Return("", assert.AnError).Once()
			}

			output, err := NewTmuxStrategy(li, m).Execute("$0")

			require.Error(t, err)
			require.Empty(t, output)
		})
	}
}
