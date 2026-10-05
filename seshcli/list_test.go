package seshcli

import (
	"bytes"
	"context"
	"testing"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListSSHOnly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mockLister := lister.NewMockLister(t)
	mockLister.On("List", lister.ListOptions{Context: ctx, SSH: true, IconExcludes: []string{}}).Return(model.SeshSessions{
		OrderedIndex: []string{"ssh:ssh/server/0"},
		Directory:    model.SeshSessionMap{"ssh:ssh/server/0": {Src: "ssh", Name: "ssh/server/0"}},
	}, nil).Once()
	cmd := newListCommand(nil, func(*cobra.Command, *BaseDeps) (*Deps, error) {
		return &Deps{Config: model.Config{SSH: model.SSHConfig{Hosts: []string{"server"}}}, Lister: mockLister}, nil
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--ssh"})

	require.NoError(t, cmd.ExecuteContext(ctx))
	assert.Equal(t, "ssh/server/0\n", output.String())
}

func TestListSSHSourceSelection(t *testing.T) {
	for _, tt := range sshSourceSelectionCases() {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			wantOpts := tt.opts
			wantOpts.Context = ctx
			wantOpts.IconExcludes = []string{}
			mockLister := lister.NewMockLister(t)
			mockLister.On("List", wantOpts).Return(tt.sessions, nil).Once()
			deps := &Deps{
				BaseDeps: BaseDeps{Exec: execwrap.NewMockExec(t)},
				Config:   tt.config,
				Lister:   mockLister,
			}
			cmd := newListCommand(nil, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetArgs(tt.args)

			require.NoError(t, cmd.ExecuteContext(ctx))
			assert.Equal(t, tt.output, output.String())
		})
	}
}

func TestListSSHAndPanes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mockTmux := tmux.NewMockTmux(t)
	mockTmux.On("IsAttached").Return(true).Once()
	mockLister := lister.NewMockLister(t)
	mockLister.On("List", lister.ListOptions{Context: ctx, SSH: true, Panes: true, IconExcludes: []string{}}).Return(model.SeshSessions{
		OrderedIndex: []string{"tmux-pane:host/window/1", "ssh:ssh/server/0"},
		Directory: model.SeshSessionMap{
			"tmux-pane:host/window/1": {Src: "tmux-pane", Name: "host/window/1"},
			"ssh:ssh/server/0":        {Src: "ssh", Name: "ssh/server/0"},
		},
	}, nil).Once()
	cmd := newListCommand(nil, func(*cobra.Command, *BaseDeps) (*Deps, error) {
		return &Deps{Lister: mockLister, Tmux: mockTmux}, nil
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetArgs([]string{"--ssh", "--panes"})

	require.NoError(t, cmd.ExecuteContext(ctx))
	assert.Equal(t, "host/window/1\nssh/server/0\n", output.String())
}

type sshSourceSelectionCase struct {
	name     string
	config   model.Config
	args     []string
	opts     lister.ListOptions
	sessions model.SeshSessions
	output   string
}

// Both commands delegate default source selection to the same lister. Explicit
// filters keep every selected flag rather than choosing a single source.
func sshSourceSelectionCases() []sshSourceSelectionCase {
	configured := model.Config{SSH: model.SSHConfig{Hosts: []string{"server"}}}
	emptyConfig := model.Config{SSH: model.SSHConfig{Hosts: []string{}}}
	local := model.SeshSessions{
		OrderedIndex: []string{"tmux:local"},
		Directory:    model.SeshSessionMap{"tmux:local": {Src: "tmux", Name: "local", Path: "/local"}},
	}
	mixed := model.SeshSessions{
		OrderedIndex: []string{"tmux:local", "ssh:ssh/server/0"},
		Directory: model.SeshSessionMap{
			"tmux:local":       {Src: "tmux", Name: "local", Path: "/local"},
			"ssh:ssh/server/0": {Src: "ssh", Name: "ssh/server/0"},
		},
	}
	return []sshSourceSelectionCase{
		{"unconfigured defaults", model.Config{}, nil, lister.ListOptions{}, local, "local\n"},
		{"empty SSH defaults", emptyConfig, nil, lister.ListOptions{}, local, "local\n"},
		{"configured defaults", configured, nil, lister.ListOptions{}, mixed, "local\nssh/server/0\n"},
		{"local filter with SSH configured", configured, []string{"--tmux"}, lister.ListOptions{Tmux: true}, local, "local\n"},
		{"SSH and local union", configured, []string{"--ssh", "--tmux"}, lister.ListOptions{SSH: true, Tmux: true}, mixed, "local\nssh/server/0\n"},
		{"all source filters", configured, []string{"--ssh", "--tmux", "--config", "--zoxide", "--tmuxinator"},
			lister.ListOptions{SSH: true, Tmux: true, Config: true, Zoxide: true, Tmuxinator: true}, mixed, "local\nssh/server/0\n"},
		{"SSH without config", model.Config{}, []string{"--ssh"}, lister.ListOptions{SSH: true}, model.SeshSessions{}, ""},
		{"SSH with empty hosts", emptyConfig, []string{"--ssh"}, lister.ListOptions{SSH: true}, model.SeshSessions{}, ""},
	}
}

func TestListSSHFlag(t *testing.T) {
	cmd := NewListCommand(nil)
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

func TestListFormattingFlags(t *testing.T) {
	t.Run("format defaults to empty", func(t *testing.T) {
		cmd := NewListCommand(nil)

		format, err := cmd.Flags().GetString("format")

		require.NoError(t, err)
		assert.Empty(t, format)
	})

	t.Run("icons exclude defaults to empty", func(t *testing.T) {
		cmd := NewListCommand(nil)

		excluded, err := cmd.Flags().GetStringSlice("icons-exclude")

		require.NoError(t, err)
		assert.Empty(t, excluded)
	})

	t.Run("accepts format and repeated icon exclusions", func(t *testing.T) {
		cmd := NewListCommand(nil)

		require.NoError(t, cmd.Flags().Parse([]string{
			"--format",
			"{session}\x1f{active_window_name_prefix}{name}",
			"--icons-exclude",
			"tmux",
			"--icons-exclude",
			"config",
		}))

		format, err := cmd.Flags().GetString("format")
		require.NoError(t, err)
		assert.Equal(
			t,
			"{session}\x1f{active_window_name_prefix}{name}",
			format,
		)

		excluded, err := cmd.Flags().GetStringSlice("icons-exclude")
		require.NoError(t, err)
		assert.Equal(t, []string{"tmux", "config"}, excluded)
	})
}
