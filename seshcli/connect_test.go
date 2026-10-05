package seshcli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/joshmedeski/sesh/v2/connector"
	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/focuser"
	"github.com/joshmedeski/sesh/v2/icon"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestJSONNamePassedUnchangedToConnectSwitch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	deps := sshJSONTestDeps(t, []string{"first"}, map[string]execwrap.CommandResult{
		"first": {Stdout: []byte("nutiliti/2345% café\n")},
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
		Name:        bridge,
		Command:     `exec ssh -tt -- 'first' 'tmux attach-session -t '\''=nutiliti/2345% café'\'''`,
		Environment: []string{"SESH_SSH_IDENTITY=ssh/first/nutiliti%2F2345%25%20caf%C3%A9"},
	}).Return("", nil).Once()
	mTmux.EXPECT().IsAttached().Return(false).Once()
	mTmux.EXPECT().ResolveClient().Return("/dev/ttys002").Once()
	mTmux.EXPECT().SwitchClientTarget("/dev/ttys002", bridge).Return("", nil).Once()
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
