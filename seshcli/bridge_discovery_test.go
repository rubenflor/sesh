package seshcli

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/home"
	seshjson "github.com/joshmedeski/sesh/v2/json"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/picker"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/tmuxinator"
	"github.com/joshmedeski/sesh/v2/zoxide"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestListHidesMarkedSSHBridges(t *testing.T) {
	for _, tt := range []struct {
		name  string
		args  []string
		names []string
	}{
		{"default", nil, []string{"local", "sesh-ssh-user"}},
		{"tmux", []string{"--tmux"}, []string{"local", "sesh-ssh-user"}},
		{"JSON", []string{"--json"}, []string{"local", "sesh-ssh-user"}},
		{"tmux JSON", []string{"--tmux", "--json"}, []string{"local", "sesh-ssh-user"}},
		{"panes", []string{"--panes"}, []string{"editor/zsh", "sesh-ssh-user/zsh"}},
		{"panes JSON", []string{"--panes", "--json"}, []string{"editor/zsh", "sesh-ssh-user/zsh"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			deps := bridgeDiscoveryTestDeps(t)
			cmd := newListCommand(&deps.BaseDeps, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
			var stdout, stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			cmd.SetArgs(tt.args)

			require.NoError(t, cmd.ExecuteContext(context.Background()))
			assert.Empty(t, stderr.String())
			if !slices.Contains(tt.args, "--json") {
				assert.Equal(t, strings.Join(tt.names, "\n")+"\n", stdout.String())
				return
			}

			var rows []map[string]any
			require.NoError(t, json.Unmarshal(stdout.Bytes(), &rows))
			require.Len(t, rows, len(tt.names))
			var legacyRows []map[string]any
			require.NoError(t, json.Unmarshal([]byte(legacyLocalListJSON), &legacyRows))
			for i, row := range rows {
				assert.Equal(t, tt.names[i], row["Name"])
				assert.Len(t, row, len(legacyRows[0]), "internal bridge metadata must not add public JSON fields")
				for key := range legacyRows[0] {
					assert.Contains(t, row, key, "existing public JSON fields must remain")
				}
			}
		})
	}
}

func TestPickerInputHidesMarkedSSHBridges(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
	}{
		{"default", nil},
		{"tmux", []string{"--tmux"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			deps := bridgeDiscoveryTestDeps(t)
			mockPicker := picker.NewMockPicker(t)
			mockPicker.On("Pick", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				sessions, err := args.Get(0).(picker.FetchFunc)()
				require.NoError(t, err)
				assert.Equal(t, []string{"tmux:local", "tmux:sesh-ssh-user"}, sessions.OrderedIndex)
				assert.Equal(t, model.SeshSessionMap{
					"tmux:local":         {Src: "tmux", Name: "local", Path: "/local", TmuxSessionID: "$1", TmuxServerGeneration: "101:1700000000:/tmp/tmux-fixture"},
					"tmux:sesh-ssh-user": {Src: "tmux", Name: "sesh-ssh-user", Path: "/user", TmuxSessionID: "$3", TmuxServerGeneration: "101:1700000000:/tmp/tmux-fixture"},
				}, sessions.Directory)
			}).Return("", nil).Once()
			deps.Picker = mockPicker
			cmd := newPickerCommand(&deps.BaseDeps, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
			cmd.SetArgs(tt.args)

			require.NoError(t, cmd.ExecuteContext(context.Background()))
		})
	}
}

func bridgeDiscoveryTestDeps(t *testing.T) *Deps {
	t.Helper()
	mockTmux := tmux.NewMockTmux(t)
	mockTmux.On("ListSessions").Return([]*model.TmuxSession{
		{ID: "$1", ServerGeneration: "101:1700000000:/tmp/tmux-fixture", Name: "local", Path: "/local"},
		{ID: "$2", Name: "renamed-bridge", Path: "/bridge", SSHBridgeIdentity: "ssh/host/work"},
		{ID: "$3", ServerGeneration: "101:1700000000:/tmp/tmux-fixture", Name: "sesh-ssh-user", Path: "/user"},
	}, nil).Maybe()
	mockTmux.On("ListTmuxPanes").Return([]*model.TmuxPane{
		{SessionID: "$1", WindowName: "editor", PaneCommand: "zsh", PanePath: "/local", PaneID: "%1"},
		{SessionID: "$2", WindowName: "renamed-bridge", PaneCommand: "ssh", PanePath: "/bridge", PaneID: "%2", SSHBridgeIdentity: "ssh/host/work"},
		{SessionID: "$3", WindowName: "sesh-ssh-user", PaneCommand: "zsh", PanePath: "/user", PaneID: "%3"},
	}, nil).Maybe()
	mockTmux.On("IsAttached").Return(true).Maybe()
	mockTmux.On("ListAllWindows").Return(map[string][]model.TmuxWindow(nil), nil).Maybe()
	mockTmuxinator := tmuxinator.NewMockTmuxinator(t)
	mockTmuxinator.On("List").Return([]*model.TmuxinatorConfig{}, nil).Maybe()
	mockZoxide := zoxide.NewMockZoxide(t)
	mockZoxide.On("ListResults").Return([]*model.ZoxideResult{}, nil).Maybe()
	return &Deps{
		BaseDeps: BaseDeps{Json: seshjson.NewJson()},
		Tmux:     mockTmux,
		Lister:   lister.NewLister(model.Config{}, home.NewMockHome(t), mockTmux, mockZoxide, mockTmuxinator, nil, nil, nil),
	}
}
