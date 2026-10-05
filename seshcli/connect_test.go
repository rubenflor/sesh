package seshcli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/joshmedeski/sesh/v2/connector"
	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/icon"
	"github.com/joshmedeski/sesh/v2/model"
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

	mockConnector := connector.NewMockConnector(t)
	mockConnector.On("Connect", "ssh/first/nutiliti%2F2345%25%20caf%C3%A9", model.ConnectOpts{Switch: true}).Return("", nil).Once()
	deps.Connector = mockConnector
	deps.Icon = icon.NewIcon(deps.Config)
	connect := newConnectCommand(&deps.BaseDeps, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
	// This is the unchanged Raycast invocation, using the JSON row's Name.
	connect.SetArgs([]string{"--switch", rows[0].Name})
	require.NoError(t, connect.ExecuteContext(ctx))
}
