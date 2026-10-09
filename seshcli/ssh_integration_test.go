package seshcli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/cache"
	"github.com/joshmedeski/sesh/v2/dir"
	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/picker"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/tmuxinator"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const integrationSSHIdentity = "ssh/first/nutiliti%2F2345%25%20caf%C3%A9"
const integrationUserCollision = "sesh-ssh-f89c7664900e54d378d5c534-0"

func sessionInventoryRow(fields []string) string {
	quoted := append([]string(nil), fields...)
	for _, i := range []int{1, 3, 6, 8, 9, 17, 18, 19, 21} {
		var value strings.Builder
		value.WriteByte('"')
		for j := 0; j < len(fields[i]); j++ {
			if strings.IndexByte("|&;<>()$`\\\"'*?[# =%", fields[i][j]) >= 0 {
				value.WriteByte('\\')
			}
			value.WriteByte(fields[i][j])
		}
		value.WriteByte('"')
		quoted[i] = value.String()
	}
	return "S1::" + strings.Join(quoted, "::")
}

// Load a real config and dependency graph. Only external process, filesystem
// resolution, and interactive picker boundaries are replaced; tmux metadata
// parsing, source aggregation, caching, and connection dispatch stay real.
func sshIntegrationDeps(t *testing.T, caching bool) (*Deps, *shell.MockShell, *execwrap.MockExec, string) {
	t.Helper()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("TMUX", "/tmp/sesh-test,1,1")
	t.Setenv("TMUX_PANE", "%1")
	t.Setenv("SESH_CLIENT", "")
	configPath := filepath.Join(t.TempDir(), "sesh.toml")
	config := "cache = false\n"
	if caching {
		config = "cache = true\n"
	}
	config += "sort_order = ['tmux', 'config', 'ssh']\n[ssh]\nhosts = ['first', 'empty', 'failed']\n" +
		"[[session]]\nname = 'alias-target'\npath = '/alias'\nalias = '" + integrationSSHIdentity + "'\n"
	require.NoError(t, os.WriteFile(configPath, []byte(config), 0600))
	runner := execwrap.NewMockExec(t)
	for host, result := range map[string]execwrap.CommandResult{
		"first":  {Stdout: []byte("sesh-ssh-inventory-v1\nnutiliti/2345% café\n"), Stderr: []byte("successful probe warning\n")},
		"empty":  {Stdout: []byte("sesh-ssh-inventory-v1\n")},
		"failed": {ExitCode: 255, Stderr: []byte("connection refused\n")},
	} {
		var err error
		if result.ExitCode != 0 {
			err = assert.AnError
		}
		runner.On("RunContext", mock.Anything, "ssh", "-T", "-n", "-o", "BatchMode=yes", "--", host, mock.Anything).
			Return(result, err).Maybe()
	}
	s := shell.NewMockShell(t)
	s.On("CmdCaptureRaw", mock.Anything, "tmux", 16<<20, 64<<10, "-u", "list-sessions", "-F", mock.Anything).Return(rawInventoryResult(strings.Join([]string{
		sessionInventoryRow([]string{"300", "", "1", "", "100", "0", "", "0", "", "", "0", "0", "0", "$1", "300", "0", "0", "renamed-bridge", "/bridge", "", "1", "101:1700000000:/tmp/tmux-fixture"}),
		sessionInventoryRow([]string{"200", "", "0", "", "100", "0", "", "0", "", "", "0", "0", "0", "$2", "200", "0", "0", integrationUserCollision, "/user", "", "1", "101:1700000000:/tmp/tmux-fixture"}),
	}, "\n")), nil).Maybe()
	s.On("CmdCapture", "tmux", "show-environment", "-t", "$1", "--", model.SSHBridgeIdentityEnv).
		Return(model.SSHBridgeIdentityEnv+"="+integrationSSHIdentity, nil).Maybe()
	s.On("CmdCapture", "tmux", "show-environment", "-t", "$2", "--", model.SSHBridgeIdentityEnv).
		Return("", nil).Maybe()
	s.On("ListCmd", "tmux", "list-windows", "-a", "-F", mock.Anything).Return([]string{}, nil).Maybe()
	s.On("ListCmd", "tmux", "-u", "list-windows", "-a", "-F", mock.Anything).Return([]string{}, nil).Maybe()
	s.On("ListCmd", "tmuxinator", "list", "-n").Return(nil, assert.AnError).Maybe()
	s.On("PrepareCmd", "zoxide query --list --score", map[string]string{}).
		Return([]string{"zoxide", "query", "--list", "--score"}, nil).Maybe()
	s.On("ListCmd", "zoxide", "query", "--list", "--score").Return([]string{}, nil).Maybe()
	s.On("Cmd", "tmux", "display-message", "-p", "#{session_id}::#{pid}:#{start_time}:#{socket_path}").Return("$1::101:1700000000:/tmp/tmux-fixture", nil).Maybe()
	s.On("Cmd", "tmux", "display-message", "-p", "-t", "$1:", "#{session_id}::#{pid}:#{start_time}:#{socket_path}").Return("$1::101:1700000000:/tmp/tmux-fixture", nil).Maybe()
	base := NewBaseDepsWithExec(runner)
	base.Shell = s
	base.Tmuxinator = tmuxinator.NewTmuxinator(s)
	base.Dir = dir.NewMockDir(t) // Canonical resolution must never inspect a path.
	deps, err := base.BuildAll(configPath)
	require.NoError(t, err)
	return deps, s, runner, configPath
}

func TestSSHIntegrationListHandoffAndCache(t *testing.T) {
	deps, s, runner, _ := sshIntegrationDeps(t, true)
	require.NotNil(t, deps.CachingLister, "root config must enable the cache wrapper")
	build := func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil }
	fc := cache.NewFileCache()
	cached := model.SeshSessions{
		OrderedIndex: []string{"tmux:cached"},
		Directory:    model.SeshSessionMap{"tmux:cached": {Src: "tmux", Name: "cached"}},
	}
	require.NoError(t, fc.Write(cached))
	before, err := fc.Read()
	require.NoError(t, err)
	want := []string{integrationUserCollision, "alias-target", integrationSSHIdentity}
	var selected string
	for _, args := range [][]string{{"--json"}, {}} {
		cmd := newListCommand(&deps.BaseDeps, build)
		var out, stderr bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&stderr)
		cmd.SetArgs(args)
		require.NoError(t, cmd.ExecuteContext(t.Context()))
		assert.Empty(t, stderr.String())
		if len(args) == 0 {
			assert.Equal(t, strings.Join(want, "\n")+"\n", out.String())
			continue
		}
		var rows []model.SeshSession
		require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
		require.Len(t, rows, len(want))
		for i, row := range rows {
			assert.Equal(t, want[i], row.Name)
		}
		assert.Equal(t, "ssh", rows[2].Src)
		assert.Empty(t, rows[2].Path)
		selected = rows[2].Name // Integration callers pass the JSON Name verbatim.
	}
	after, err := fc.Read()
	require.NoError(t, err)
	assert.Equal(t, before, after, "SSH-inclusive lists must neither read nor rewrite the local cache")
	runner.AssertNumberOfCalls(t, "RunContext", 6)

	s.On("ListCmd", "tmux", "list-clients", "-F", mock.Anything).
		Return([]string{"client::/dev/ttys001::$1::300"}, nil).Once()
	s.On("CmdCapture", "tmux", "display-message", "-p", "-t", "$1:", "#{pane_dead}").Return("0", nil).Twice()
	s.On("Cmd", "tmux", "switch-client", "-t", "$1").Return("", nil).Twice()
	connect := newConnectCommand(&deps.BaseDeps, build)
	connect.SetArgs([]string{"--switch", selected})
	require.NoError(t, connect.ExecuteContext(t.Context()))
	deps.CachingLister.Wait()
	runner.AssertNumberOfCalls(t, "RunContext", 6) // Connect/refresh must not probe SSH.

	local := newListCommand(&deps.BaseDeps, build)
	var localOut bytes.Buffer
	local.SetOut(&localOut)
	local.SetArgs([]string{"--tmux"})
	// A cache hit needs no tmux inventory, even though the real inner lister exists.
	listCalls := len(s.Calls)
	require.NoError(t, local.ExecuteContext(t.Context()))
	assert.Equal(t, integrationUserCollision+"\n", localOut.String())
	assert.Len(t, s.Calls, listCalls)
	runner.AssertNumberOfCalls(t, "RunContext", 6)
	stored, err := fc.Read()
	require.NoError(t, err)
	assert.Equal(t, []string{"tmux:" + integrationUserCollision, "config:alias-target"}, stored.Sessions.OrderedIndex)
	assert.NotContains(t, stored.Sessions.Directory, "tmux:renamed-bridge")
	assert.NotContains(t, stored.Sessions.Directory, "ssh:"+selected)

	pk := picker.NewMockPicker(t)
	pk.EXPECT().Pick(mock.Anything, mock.Anything).RunAndReturn(func(fetch picker.FetchFunc, _ picker.PickerOptions) (model.SeshSession, error) {
		sessions, err := fetch()
		require.NoError(t, err)
		assert.Equal(t, []string{"tmux:" + integrationUserCollision, "config:alias-target", "ssh:" + selected}, sessions.OrderedIndex)
		assert.NotContains(t, sessions.Directory, "tmux:renamed-bridge")
		return sessions.Directory["ssh:"+selected], nil
	}).Once()
	deps.Picker = pk
	require.NoError(t, newPickerCommand(&deps.BaseDeps, build).ExecuteContext(t.Context()))
	runner.AssertNumberOfCalls(t, "RunContext", 9)
	unchanged, err := fc.Read()
	require.NoError(t, err)
	assert.Equal(t, stored, unchanged, "picker SSH discovery must bypass cache writes too")
}

func TestSSHIntegrationBridgeMetadataNoLeak(t *testing.T) {
	deps, s, runner, configPath := sshIntegrationDeps(t, false)
	build := func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil }
	s.On("CmdCaptureRaw", mock.Anything, "tmux", 16<<20, 64<<10, "-u", "list-panes", "-s", "-F", mock.Anything).Return(rawInventoryResult(strings.Join([]string{
		`P1::0::"remote"::0::""::"ssh"::"/bridge"::%1::$1::@1::"101:1700000000:/tmp/tmux-fixture"`,
		`P1::0::"user"::0::""::"zsh"::"/user"::%2::$2::@2::"101:1700000000:/tmp/tmux-fixture"`,
	}, "\n")), nil).Once()
	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{"default", nil, integrationUserCollision + "\nalias-target\n" + integrationSSHIdentity + "\n"},
		{"tmux", []string{"--tmux"}, integrationUserCollision + "\n"},
		{"panes", []string{"--panes", "--ssh"}, integrationSSHIdentity + "\nuser/zsh\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newListCommand(&deps.BaseDeps, build)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(tt.args)
			require.NoError(t, cmd.ExecuteContext(t.Context()))
			assert.Equal(t, tt.want, out.String())
		})
	}
	_, found := deps.Lister.FindTmuxSession("renamed-bridge")
	assert.False(t, found)
	user, found := deps.Lister.FindTmuxSession(integrationUserCollision)
	require.True(t, found)
	assert.Equal(t, "/user", user.Path)
	_, found = deps.Lister.GetAttachedTmuxSession()
	assert.False(t, found, "the attached marked session must not become a local target")
	_, found = deps.Lister.GetLastTmuxSession()
	assert.False(t, found, "an internal bridge must not supply a second local session")

	// Window commands build their own real dependencies from the same config;
	// neither implicit nor explicit targets may reach list-windows on a bridge.
	for _, args := range [][]string{{"list"}, {"list", "--target", "renamed-bridge"}, {"list", "--json", "--target", "renamed-bridge"}} {
		t.Run("window "+strings.Join(args, " "), func(t *testing.T) {
			root := &cobra.Command{Use: "sesh", SilenceErrors: true, SilenceUsage: true}
			root.PersistentFlags().String("config", configPath, "")
			root.AddCommand(NewWindowCommand(&deps.BaseDeps))
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(append([]string{"window"}, args...))
			err := root.ExecuteContext(t.Context())
			if len(args) == 1 {
				require.ErrorContains(t, err, "use --target")
			} else {
				require.ErrorContains(t, err, "not found")
			}
			assert.Empty(t, out.String())
		})
	}
	s.On("ListCmd", "tmux", "-u", "if-shell", "-F", "-t", "$2:", mock.Anything,
		"'list-windows' '-t' '$2' '-F' '#{window_id}::#{window_index}::#{window_active}::#{n:window_name}::#{n:pane_current_path}::#{n:#{pid}:#{start_time}:#{socket_path}}::#{window_name}#{pane_current_path}#{pid}:#{start_time}:#{socket_path}'", "if-shell").
		Return([]string{}, nil).Once()
	root := &cobra.Command{Use: "sesh"}
	root.PersistentFlags().String("config", configPath, "")
	root.AddCommand(NewWindowCommand(&deps.BaseDeps))
	root.SetArgs([]string{"window", "list", "--target", integrationUserCollision})
	require.NoError(t, root.ExecuteContext(t.Context()), "unmarked collision remains a valid local window target")
	runner.AssertNumberOfCalls(t, "RunContext", 6)
}

func TestSSHIntegrationInvalidIdentityNeverFallsBack(t *testing.T) {
	for _, tt := range []struct {
		identity string
		message  string
	}{
		{"ssh/first/nutiliti%2f2345", "canonical"},
		{"ssh/first/%7E", "canonical"},
		{"ssh/missing/work", "not configured"},
	} {
		t.Run(tt.identity, func(t *testing.T) {
			deps, s, runner, _ := sshIntegrationDeps(t, true)
			cmd := newConnectCommand(&deps.BaseDeps, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
			cmd.SilenceErrors, cmd.SilenceUsage = true, true
			cmd.SetArgs([]string{"--switch", tt.identity})
			require.ErrorContains(t, cmd.ExecuteContext(t.Context()), tt.message)
			assert.Empty(t, s.Calls, "invalid identities must not reach pane, bridge, or local resolution")
			assert.Empty(t, runner.Calls)
		})
	}
}
