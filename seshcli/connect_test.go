package seshcli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/connector"
	"github.com/joshmedeski/sesh/v2/dir"
	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/focuser"
	"github.com/joshmedeski/sesh/v2/icon"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestConnectSSHRejectsUnsupportedFlagsBeforeLocalOperations(t *testing.T) {
	for _, flags := range [][]string{
		{"--command", "echo hello"},
		{"--command", "echo hello", "--switch"},
		{"--root"},
		{"--root", "--switch"},
	} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			config := model.Config{SSH: model.SSHConfig{Hosts: []string{"host"}}}
			// No expectations: any local path or tmux operation fails immediately.
			base := BaseDeps{Dir: dir.NewMockDir(t)}
			deps := &Deps{BaseDeps: base, Config: config, Icon: icon.NewIcon(config)}
			deps.Connector = connector.NewConnector(config,
				base.Dir, nil, nil, nil, nil, tmux.NewMockTmux(t), nil, nil, nil,
			)
			cmd := newConnectCommand(&base, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			cmd.SetArgs(append(flags, "ssh/host/0"))

			require.EqualError(t, cmd.Execute(), flags[0]+" is not supported for SSH sessions")
		})
	}
}

func TestConnectRootPreservesLocalPathResolution(t *testing.T) {
	base := BaseDeps{Dir: dir.NewMockDir(t)}
	base.Dir.(*dir.MockDir).EXPECT().RootDir("/repo/subdir").Return(true, "/repo").Once()
	mConnector := connector.NewMockConnector(t)
	mConnector.EXPECT().Connect("/repo", model.ConnectOpts{Switch: true}).Return("connected", nil).Once()
	deps := &Deps{BaseDeps: base, Connector: mConnector, Icon: icon.NewIcon(model.Config{})}
	cmd := newConnectCommand(&base, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
	cmd.SetArgs([]string{"--root", "--switch", "/repo/subdir"})

	require.NoError(t, cmd.Execute())
}

func TestConnectRootRejectsSSHBeforePathResolution(t *testing.T) {
	for _, tt := range []struct{ target, wantError string }{
		{" ssh/host/work%2F1", "--root is not supported for SSH sessions"},
		{"ssh/host/work%2f1", "non-canonical encoding"},
	} {
		t.Run(tt.target, func(t *testing.T) {
			base := BaseDeps{Dir: dir.NewMockDir(t)}
			deps := &Deps{Connector: connector.NewMockConnector(t), Icon: icon.NewIcon(model.Config{})}
			cmd := newConnectCommand(&base, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			cmd.SetArgs([]string{"--root", tt.target})

			require.ErrorContains(t, cmd.Execute(), tt.wantError)
		})
	}
}

func TestJSONNamePassedUnchangedToConnectSwitch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	deps := sshJSONTestDeps(t, []string{"first"}, map[string]execwrap.CommandResult{
		"first": {Stdout: []byte("sesh-ssh-inventory-v1\nnutiliti/2345% café\n")},
	})
	list := newListCommand(&deps.BaseDeps, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
	var stdout bytes.Buffer
	list.SetOut(&stdout)
	list.SetArgs([]string{"--json", "--hide-duplicates", "--ssh"})
	require.NoError(t, list.ExecuteContext(ctx))
	var rows []model.SeshSession
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &rows))
	require.Len(t, rows, 1)
	require.Equal(t, "ssh/first/nutiliti%2F2345%25%20caf%C3%A9", rows[0].Name)
	require.Equal(t, "ssh", rows[0].Src)
	require.Empty(t, rows[0].Path)

	const bridge = "sesh-ssh-f89c7664900e54d378d5c534-0"
	mTmux := tmux.NewMockTmux(t)
	mTmux.EXPECT().ListClients().Return([]model.TmuxClient{{Name: "/dev/ttys002"}}, nil).Twice()
	mTmux.EXPECT().ListSessions().Return(nil, nil).Once()
	mTmux.EXPECT().NewSessionWithCommand(model.TmuxSessionOpts{
		Name:           bridge,
		Command:        `exec ssh -tt -- 'first' 'tmux attach-session -t '\''=nutiliti/2345% café'\'''`,
		Environment:    []string{"SESH_SSH_IDENTITY=ssh/first/nutiliti%2F2345%25%20caf%C3%A9"},
		SessionOptions: []model.TmuxOption{{Name: "status", Value: "off"}, {Name: "detach-on-destroy", Value: "off"}},
		PaneOptions:    []model.TmuxOption{{Name: "remain-on-exit", Value: "failed"}, {Name: "remain-on-exit-format", Value: "SSH exited (status #{pane_dead_status}). Reconnect with sesh to retry."}},
	}).Return("$8 %9", nil).Once()
	mTmux.EXPECT().IsAttached().Return(false).Once()
	mTmux.EXPECT().ResolveClient().Return("/dev/ttys002").Once()
	mTmux.EXPECT().SwitchClientTarget("/dev/ttys002", "$8").Return("", nil).Once()
	mFocuser := focuser.NewMockFocuser(t)
	mFocuser.EXPECT().Activate("").Return(true, nil).Once()
	deps.Connector = connector.NewConnector(deps.Config,
		nil, nil, deps.Lister, nil, nil, mTmux, nil, nil, mFocuser,
	)
	deps.Icon = icon.NewIcon(deps.Config)
	connect := newConnectCommand(&deps.BaseDeps, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
	// This is the unchanged Raycast invocation, using the JSON row's Name.
	connect.SetArgs([]string{"--switch", rows[0].Name})
	require.NoError(t, connect.ExecuteContext(ctx))
}
