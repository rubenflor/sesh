package seshcli

import (
	"context"
	"testing"

	"github.com/joshmedeski/sesh/v2/connector"
	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/picker"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestPickerSSHNamePassedUnchangedToConnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	deps := sshJSONTestDeps(t, []string{"first"}, map[string]execwrap.CommandResult{
		"first": {Stdout: []byte("nutiliti/2345% café\n")},
	})
	const identity = "ssh/first/nutiliti%2F2345%25%20caf%C3%A9"
	mockPicker := picker.NewMockPicker(t)
	mockPicker.EXPECT().Pick(mock.Anything, mock.Anything).RunAndReturn(func(fetch picker.FetchFunc, _ picker.PickerOptions) (string, error) {
		sessions, err := fetch()
		require.NoError(t, err)
		require.Len(t, sessions.OrderedIndex, 1)
		selected := sessions.Directory[sessions.OrderedIndex[0]]
		assert.Equal(t, "ssh", selected.Src)
		assert.Equal(t, identity, selected.Name)
		assert.Empty(t, selected.Path)
		return selected.Name, nil
	}).Once()
	mockConnector := connector.NewMockConnector(t)
	mockConnector.On("Connect", identity, model.ConnectOpts{}).Return("", nil).Once()
	deps.Picker = mockPicker
	deps.Connector = mockConnector
	cmd := newPickerCommand(&deps.BaseDeps, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
	cmd.SetArgs([]string{"--ssh"})

	require.NoError(t, cmd.ExecuteContext(ctx))
}

func TestPickerSSHOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sessions := model.SeshSessions{
		OrderedIndex: []string{"ssh:ssh/server/0"},
		Directory:    model.SeshSessionMap{"ssh:ssh/server/0": {Src: "ssh", Name: "ssh/server/0"}},
	}
	mockLister := lister.NewMockLister(t)
	mockLister.On("List", lister.ListOptions{Context: ctx, SSH: true}).Return(sessions, nil).Once()
	mockPicker := picker.NewMockPicker(t)
	mockPicker.On("Pick", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		got, err := args.Get(0).(picker.FetchFunc)()
		require.NoError(t, err)
		assert.Equal(t, sessions, got)
	}).Return("", nil).Once()
	cmd := newPickerCommand(nil, func(*cobra.Command, *BaseDeps) (*Deps, error) {
		return &Deps{Config: model.Config{SSH: model.SSHConfig{Hosts: []string{"server"}}}, Lister: mockLister, Picker: mockPicker}, nil
	})
	cmd.SetArgs([]string{"--ssh"})

	require.NoError(t, cmd.ExecuteContext(ctx))
}

func TestPickerSSHSourceSelection(t *testing.T) {
	for _, tt := range sshSourceSelectionCases() {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			wantOpts := tt.opts
			wantOpts.Context = ctx
			mockLister := lister.NewMockLister(t)
			mockLister.On("List", wantOpts).Return(tt.sessions, nil).Once()
			mockPicker := picker.NewMockPicker(t)
			mockPicker.On("Pick", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				got, err := args.Get(0).(picker.FetchFunc)()
				require.NoError(t, err)
				assert.Equal(t, tt.sessions, got)
			}).Return("", nil).Once()
			deps := &Deps{
				BaseDeps: BaseDeps{Exec: execwrap.NewMockExec(t)},
				Config:   tt.config,
				Lister:   mockLister,
				Picker:   mockPicker,
			}
			cmd := newPickerCommand(nil, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
			cmd.SetArgs(tt.args)

			require.NoError(t, cmd.ExecuteContext(ctx))
		})
	}
}

func TestPickerSSHFlag(t *testing.T) {
	cmd := NewPickerCommand(nil)
	ssh, err := cmd.Flags().GetBool("ssh")
	require.NoError(t, err)
	assert.False(t, ssh)
	assert.Empty(t, cmd.Flags().Lookup("ssh").Shorthand)

	require.NoError(t, cmd.Flags().Parse([]string{"--ssh", "--tmux"}))
	ssh, err = cmd.Flags().GetBool("ssh")
	require.NoError(t, err)
	assert.True(t, ssh)
	tmux, err := cmd.Flags().GetBool("tmux")
	require.NoError(t, err)
	assert.True(t, tmux, "source flags must remain independently selectable")
}
