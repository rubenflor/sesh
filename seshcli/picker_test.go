package seshcli

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/connector"
	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/picker"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestPickerSSHNamePassedUnchangedToConnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	deps := sshJSONTestDeps(t, []string{"first"}, map[string]execwrap.CommandResult{
		"first": {Stdout: []byte("sesh-ssh-inventory-v1\nnutiliti/2345% café\n")},
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
	const bridge = "sesh-ssh-f89c7664900e54d378d5c534-0"
	mTmux := tmux.NewMockTmux(t)
	mTmux.EXPECT().ListSessions().Return(nil, nil).Once()
	mTmux.EXPECT().NewSessionWithCommand(model.TmuxSessionOpts{
		Name:           bridge,
		Command:        `exec ssh -tt -- 'first' 'tmux attach-session -t '\''=nutiliti/2345% café'\'''`,
		Environment:    []string{"SESH_SSH_IDENTITY=ssh/first/nutiliti%2F2345%25%20caf%C3%A9"},
		SessionOptions: []model.TmuxOption{{Name: "status", Value: "off"}, {Name: "detach-on-destroy", Value: "off"}},
		PaneOptions:    []model.TmuxOption{{Name: "remain-on-exit", Value: "failed"}, {Name: "remain-on-exit-format", Value: "SSH exited (status #{pane_dead_status}). Reconnect with sesh to retry."}},
	}).Return("$8 %9", nil).Once()
	mTmux.EXPECT().SwitchOrAttach("$8", model.ConnectOpts{}).Return("", nil).Once()
	deps.Picker = mockPicker
	deps.Connector = connector.NewConnector(deps.Config,
		nil, nil, deps.Lister, nil, nil, mTmux, nil, nil, nil,
	)
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
	mockLister.On("List", mock.MatchedBy(func(opts lister.ListOptions) bool {
		return opts.SSH && opts.Context != ctx && opts.Context.Err() == nil
	})).Return(sessions, nil).Once()
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
			mockLister.On("List", mock.MatchedBy(func(opts lister.ListOptions) bool {
				if opts.Context == nil || opts.Context == ctx {
					return false
				}
				opts.Context = ctx
				return reflect.DeepEqual(opts, wantOpts)
			})).Return(tt.sessions, nil).Once()
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

func TestPickerExitCancelsAndWaitsForDiscovery(t *testing.T) {
	for _, mode := range []string{"quit", "selection", "error", "parent cancellation"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started, cancelled, release, fetchDone := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			releaseCleanup := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseCleanup()
			ml := lister.NewMockLister(t)
			ml.On("List", mock.Anything).Run(func(args mock.Arguments) {
				discoveryCtx := args.Get(0).(lister.ListOptions).Context
				close(started)
				<-discoveryCtx.Done()
				close(cancelled)
				<-release
			}).Return(model.SeshSessions{}, context.Canceled).Once()
			pk := picker.NewMockPicker(t)
			var lateFetch picker.FetchFunc
			queuedDone := make(chan error, 1)
			pk.EXPECT().Pick(mock.Anything, mock.Anything).RunAndReturn(func(fetch picker.FetchFunc, opts picker.PickerOptions) (string, error) {
				assert.Equal(t, ctx, opts.Context)
				lateFetch = fetch
				go func() { _, _ = fetch(); close(fetchDone) }()
				<-started
				go func() { _, err := fetch(); queuedDone <- err }()
				switch mode {
				case "selection":
					return "local", nil
				case "error":
					return "", assert.AnError
				case "parent cancellation":
					cancel()
				}
				return "", nil
			})
			mc := connector.NewMockConnector(t)
			if mode == "selection" {
				mc.EXPECT().Connect("local", model.ConnectOpts{}).Run(func(string, model.ConnectOpts) {
					select {
					case <-release:
					default:
						t.Error("connected before discovery cleanup")
					}
					assert.NoError(t, ctx.Err())
				}).Return("", nil).Once()
			}
			deps := &Deps{Lister: ml, Picker: pk, Connector: mc}
			cmd := newPickerCommand(nil, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			done := make(chan error, 1)
			go func() { done <- cmd.ExecuteContext(ctx) }()
			select {
			case <-cancelled:
			case <-time.After(5 * time.Second):
				t.Fatal("discovery was not cancelled")
			}
			select {
			case err := <-done:
				t.Fatalf("returned before fetch cleanup: %v", err)
			case <-time.After(100 * time.Millisecond):
			}
			releaseCleanup()
			select {
			case err := <-done:
				if mode == "error" {
					require.ErrorIs(t, err, assert.AnError)
				} else if mode == "parent cancellation" {
					require.ErrorIs(t, err, context.Canceled)
				} else {
					require.NoError(t, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("picker did not finish cleanup")
			}
			<-fetchDone
			select {
			case err := <-queuedDone:
				require.ErrorIs(t, err, context.Canceled)
			case <-time.After(5 * time.Second):
				t.Fatal("queued fetch did not finish")
			}
			_, err := lateFetch()
			require.ErrorIs(t, err, context.Canceled, "late fetch must not reach lister")
			if mode != "parent cancellation" {
				require.NoError(t, ctx.Err())
			}
		})
	}
}
