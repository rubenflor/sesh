package connector

import (
	"testing"

	"github.com/joshmedeski/sesh/v2/dir"
	"github.com/joshmedeski/sesh/v2/focuser"
	"github.com/joshmedeski/sesh/v2/home"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/namer"
	"github.com/joshmedeski/sesh/v2/startup"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/tmuxinator"
	"github.com/joshmedeski/sesh/v2/zoxide"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sshResolver(t *testing.T, config model.Config) *RealConnector {
	t.Helper()
	// With no expectations, any local resolution, bridge, or side-effect call
	// fails the test immediately.
	return NewConnector(config,
		dir.NewMockDir(t), home.NewMockHome(t), lister.NewMockLister(t), namer.NewMockNamer(t),
		startup.NewMockStartup(t), tmux.NewMockTmux(t), zoxide.NewMockZoxide(t),
		tmuxinator.NewMockTmuxinator(t), focuser.NewMockFocuser(t),
	).(*RealConnector)
}

func TestResolveSSHBeforeAliasesAndLocalStrategies(t *testing.T) {
	const identity = "ssh/my%2Fhost/nutiliti%2F2345%252F%20caf%C3%A9"
	c := sshResolver(t, model.Config{
		SSH:            model.SSHConfig{Hosts: []string{"my/host"}},
		SessionConfigs: []model.SessionConfig{{Name: "local", Alias: identity, Path: "/local"}},
	})

	connection, err := c.Resolve(identity)

	require.NoError(t, err)
	assert.Equal(t, model.Connection{
		Found: true, Session: model.SeshSession{Src: "ssh", Name: identity},
	}, connection)
	// The same Name remains suitable for the command builder, including the
	// decoded remote session name and its literal percent-encoded-looking text.
	command, err := sshAttachCommand(connection.Session.Name)
	require.NoError(t, err)
	assert.Contains(t, command, "nutiliti/2345%2F café")
}

func TestResolveSSHUsesConfiguredHostIdentity(t *testing.T) {
	for _, identity := range []string{
		"ssh/first/0", "ssh/second/0", "ssh/-alias/0", "ssh/h%C3%B6st%20alias%25/0",
	} {
		t.Run(identity, func(t *testing.T) {
			c := sshResolver(t, model.Config{SSH: model.SSHConfig{Hosts: []string{
				"first", "second", "-alias", "höst alias%",
			}}})

			connection, err := c.Resolve(identity)

			require.NoError(t, err)
			assert.Equal(t, model.Connection{
				Found: true, Session: model.SeshSession{Src: "ssh", Name: identity},
			}, connection)
		})
	}
}

func TestResolveSSHRejectsInvalidTargetsWithoutLocalOperations(t *testing.T) {
	tests := []struct {
		name, identity, wantError string
		hosts                     []string
	}{
		{"missing components", "ssh/", "invalid SSH identity", []string{"host"}},
		{"missing session", "ssh/host", "invalid SSH identity", []string{"host"}},
		{"empty host", "ssh//0", "must not be empty", []string{"host"}},
		{"empty session", "ssh/host/", "must not be empty", []string{"host"}},
		{"extra separator", "ssh/host/work/1", "expected ssh/<host>/<session>", []string{"host"}},
		{"incomplete escape", "ssh/host/%2", "malformed percent escape", []string{"host"}},
		{"invalid escape", "ssh/host/%GG", "malformed percent escape", []string{"host"}},
		{"lowercase session escape", "ssh/host/work%2f1", "non-canonical encoding", []string{"host"}},
		{"lowercase host escape", "ssh/my%2fhost/0", "non-canonical encoding", []string{"my/host"}},
		{"encoded unreserved session", "ssh/host/%7E", "non-canonical encoding", []string{"host"}},
		{"encoded unreserved host", "ssh/%68ost/0", "non-canonical encoding", []string{"host"}},
		{"invalid UTF-8", "ssh/host/%FF", "must be valid UTF-8", []string{"host"}},
		{"raw space", "ssh/host/work space", "non-canonical encoding", []string{"host"}},
		{"unconfigured host", "ssh/other/0", `SSH host "other" is not configured in [ssh].hosts`, []string{"host"}},
		{"host prefix is not membership", "ssh/host-extra/0", `SSH host "host-extra" is not configured`, []string{"host"}},
		{"host case differs", "ssh/HOST/0", `SSH host "HOST" is not configured`, []string{"host"}},
		{"allowlist uses decoded host", "ssh/my%2Fhost/0", `SSH host "my/host" is not configured`, []string{"my%2Fhost"}},
		{"no SSH config", "ssh/host/0", `SSH host "host" is not configured`, nil},
		{"empty host list", "ssh/host/0", `SSH host "host" is not configured`, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := sshResolver(t, model.Config{
				SSH:            model.SSHConfig{Hosts: tt.hosts},
				SessionConfigs: []model.SessionConfig{{Name: "local", Alias: tt.identity}},
			})

			connection, err := c.Resolve(tt.identity)

			require.ErrorContains(t, err, tt.wantError)
			assert.Equal(t, model.Connection{}, connection)
			_, err = c.Connect(tt.identity, model.ConnectOpts{Switch: true})
			require.ErrorContains(t, err, tt.wantError)
		})
	}
}

func TestConnectWindowRejectsSSHAsLocalTarget(t *testing.T) {
	c := sshResolver(t, model.Config{SSH: model.SSHConfig{Hosts: []string{"host"}}})

	_, err := c.ConnectWindow(model.WindowConnectOpts{Session: "ssh/host/0", Name: "notes"})

	require.ErrorContains(t, err, `"ssh/host/0" is an SSH session, not a local tmux session`)
}

func TestConnectSSHRejectsCommandBeforeTmuxAccess(t *testing.T) {
	for _, switchFlag := range []bool{false, true} {
		c := sshResolver(t, model.Config{SSH: model.SSHConfig{Hosts: []string{"host"}}})

		result, err := c.Connect("ssh/host/0", model.ConnectOpts{Command: "echo hello", Switch: switchFlag})

		require.EqualError(t, err, "--command is not supported for SSH sessions")
		assert.Empty(t, result)
	}
}

func TestResolveAliasToSSHValidatesCanonicalTarget(t *testing.T) {
	tests := []struct {
		identity, wantError string
	}{
		{"ssh/host/0", ""},
		{"ssh/other/0", `SSH host "other" is not configured`},
		{"ssh/host/work%2f1", "non-canonical encoding"},
	}
	for _, tt := range tests {
		t.Run(tt.identity, func(t *testing.T) {
			c := sshResolver(t, model.Config{
				SSH:            model.SSHConfig{Hosts: []string{"host"}},
				SessionConfigs: []model.SessionConfig{{Name: tt.identity, Alias: "remote", Path: "/local"}},
			})

			connection, err := c.Resolve("remote")

			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError)
				assert.Equal(t, model.Connection{}, connection)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, model.Connection{
				Found: true, Session: model.SeshSession{Src: "ssh", Name: tt.identity},
			}, connection)
		})
	}
}
