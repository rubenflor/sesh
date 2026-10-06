package seshcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/home"
	seshjson "github.com/joshmedeski/sesh/v2/json"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/tmuxinator"
	"github.com/joshmedeski/sesh/v2/zoxide"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sshJSONCase struct {
	name      string
	hosts     []string
	args      []string
	responses map[string]execwrap.CommandResult
	names     []string
	exitCode  int
}

func sshJSONCases() []sshJSONCase {
	jsonArgs := []string{"--json", "--hide-duplicates"}
	mixedArgs := []string{"--json", "--hide-duplicates", "--tmux", "--ssh"}
	sshArgs := []string{"--json", "--hide-duplicates", "--ssh"}
	hosts := []string{"first", "second"}
	partial := map[string]execwrap.CommandResult{
		"first":  {Stdout: []byte("sesh-ssh-inventory-v1\n0\n"), Stderr: []byte("SSH warning on a successful probe\n")},
		"second": {ExitCode: 255, Stderr: []byte("connection refused\n")},
	}
	failed := map[string]execwrap.CommandResult{
		"first":  {ExitCode: 255, Stderr: []byte("authentication failed\n")},
		"second": {ExitCode: 255, Stderr: []byte("connection refused\n")},
	}
	return []sshJSONCase{
		{"absent SSH config", nil, jsonArgs, nil, []string{"local"}, 0},
		{"empty SSH config", []string{}, jsonArgs, nil, []string{"local"}, 0},
		{"duplicate remote display names", hosts, jsonArgs, map[string]execwrap.CommandResult{
			"first": {Stdout: []byte("sesh-ssh-inventory-v1\n0\n")}, "second": {Stdout: []byte("sesh-ssh-inventory-v1\n0\n")},
		}, []string{"local", "ssh/first/0", "ssh/second/0"}, 0},
		{"default partial failures", hosts, jsonArgs, partial, []string{"local", "ssh/first/0"}, 0},
		{"mixed partial failures", hosts, mixedArgs, partial, []string{"local", "ssh/first/0"}, 0},
		{"plain default partial failures", hosts, nil, partial, []string{"local", "ssh/first/0"}, 0},
		{"plain mixed partial failures", hosts, []string{"--tmux", "--ssh"}, partial, []string{"local", "ssh/first/0"}, 0},
		{"default all hosts fail", hosts, jsonArgs, failed, []string{"local"}, 0},
		{"mixed all hosts fail", hosts, mixedArgs, failed, []string{"local"}, 0},
		{"SSH-only partial failures", hosts, sshArgs, partial, []string{"ssh/first/0"}, 0},
		{"SSH-only all hosts fail", hosts, sshArgs, failed, nil, 1},
		{"SSH-only tmux status one is failure", hosts, sshArgs, map[string]execwrap.CommandResult{
			"first": {ExitCode: 1}, "second": {ExitCode: 1},
		}, nil, 1},
		{"SSH-only startup without inventory is failure", hosts, sshArgs, map[string]execwrap.CommandResult{
			"first": {}, "second": {},
		}, nil, 1},
		{"SSH-only successful empty host", hosts, sshArgs, map[string]execwrap.CommandResult{
			"first": {ExitCode: 255}, "second": {Stdout: []byte("sesh-ssh-inventory-v1\n"), Stderr: []byte("aucun serveur tmux\n")},
		}, []string{}, 0},
		{"SSH-only all hosts empty", hosts, sshArgs, map[string]execwrap.CommandResult{
			"first": {Stdout: []byte("sesh-ssh-inventory-v1\n")}, "second": {Stdout: []byte("sesh-ssh-inventory-v1\n")},
		}, []string{}, 0},
		{"SSH-only malformed stdout", hosts, sshArgs, map[string]execwrap.CommandResult{
			"first": {Stdout: []byte("sesh-ssh-inventory-v1\n0\n\n")}, "second": {ExitCode: 255},
		}, nil, 1},
		{"SSH-only without config", nil, sshArgs, nil, []string{}, 0},
		{"SSH-only empty hosts", []string{}, sshArgs, nil, []string{}, 0},
		{"encoded identity", []string{"first"}, sshArgs, map[string]execwrap.CommandResult{
			"first": {Stdout: []byte("sesh-ssh-inventory-v1\nnutiliti/2345% café\n")},
		}, []string{"ssh/first/nutiliti%2F2345%25%20caf%C3%A9"}, 0},
		{"lossless escaped identities", []string{"first"}, sshArgs, map[string]execwrap.CommandResult{
			"first": {Stdout: []byte("sesh-ssh-inventory-v1\nline\\nbreak\nline\\\\nbreak\nslash\\\\\n")},
		}, []string{"ssh/first/line%0Abreak", "ssh/first/line%5Cnbreak", "ssh/first/slash%5C"}, 0},
	}
}

// Running the command in a helper process captures both Cobra and global slog
// output and proves the CLI's actual exit status without a live SSH server.
func TestListSSHJSONProcessCompatibility(t *testing.T) {
	for i, tt := range sshJSONCases() {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			t.Cleanup(cancel)
			process := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestListSSHJSONHelperProcess$")
			process.Env = append(os.Environ(), "SESH_SSH_JSON_HELPER="+strconv.Itoa(i))
			var stdout, stderr bytes.Buffer
			process.Stdout, process.Stderr = &stdout, &stderr
			err := process.Run()
			require.NoError(t, ctx.Err(), "helper process must finish before the watchdog")
			if tt.exitCode != 0 {
				var exitError *exec.ExitError
				require.ErrorAs(t, err, &exitError)
				assert.Equal(t, tt.exitCode, exitError.ExitCode(), stderr.String())
				assert.Contains(t, stderr.String(), "all configured SSH hosts failed")
				for _, host := range tt.hosts {
					assert.Contains(t, stderr.String(), host)
				}
				return
			}
			require.NoError(t, err, stderr.String())
			assert.Empty(t, stderr.String(), "successful listing must not warn on stderr")
			if !slices.Contains(tt.args, "--json") {
				assert.Equal(t, strings.Join(tt.names, "\n")+"\n", stdout.String())
				return
			}
			assertSSHJSONRows(t, stdout.Bytes(), tt.names)
			if len(tt.hosts) == 0 && !slices.Contains(tt.args, "--ssh") {
				assert.Equal(t, legacyLocalListJSON, stdout.String(), "local JSON is byte-for-byte compatible")
			}
		})
	}
}

func assertSSHJSONRows(t *testing.T, output []byte, names []string) {
	t.Helper()
	var rows []json.RawMessage
	require.NoError(t, json.Unmarshal(output, &rows), "stdout must contain only one flat JSON array")
	require.NotNil(t, rows, "an empty inventory is [] rather than null")
	require.Len(t, rows, len(names))
	var legacyRows []json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(legacyLocalListJSON), &legacyRows))
	var legacyFields map[string]any
	require.NoError(t, json.Unmarshal(legacyRows[0], &legacyFields))
	for i, row := range rows {
		var fields map[string]any
		require.NoError(t, json.Unmarshal(row, &fields))
		assert.Equal(t, names[i], fields["Name"])
		if names[i] == "local" {
			assert.Equal(t, string(legacyRows[0]), string(row), "SSH does not change local row fields or values")
			continue
		}
		assert.Equal(t, "ssh", fields["Src"])
		assert.Equal(t, "", fields["Path"])
		assert.Len(t, fields, len(legacyFields), "SSH must not add public fields")
		for key, value := range legacyFields {
			if key != "Src" && key != "Name" && key != "Path" {
				actual, exists := fields[key]
				assert.True(t, exists, "existing field %s is preserved", key)
				assert.Equal(t, value, actual, "SSH field %s keeps its zero value", key)
			}
		}
	}
}

func TestListSSHJSONHelperProcess(t *testing.T) {
	index := os.Getenv("SESH_SSH_JSON_HELPER")
	if index == "" {
		return
	}
	i, err := strconv.Atoi(index)
	if err != nil || i < 0 || i >= len(sshJSONCases()) {
		panic("invalid SSH JSON helper scenario")
	}
	tt := sshJSONCases()[i]
	deps := sshJSONTestDeps(t, tt.hosts, tt.responses)
	cmd := newListCommand(&deps.BaseDeps, func(*cobra.Command, *BaseDeps) (*Deps, error) { return deps, nil })
	cmd.SetOut(os.Stdout)
	cmd.SetErr(os.Stderr)
	cmd.SetArgs(tt.args)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func sshJSONTestDeps(t *testing.T, hosts []string, responses map[string]execwrap.CommandResult) *Deps {
	t.Helper()
	config := model.Config{SSH: model.SSHConfig{Hosts: hosts}}
	mockTmux := tmux.NewMockTmux(t)
	mockTmux.On("ListSessions").Return([]*model.TmuxSession{{Name: "local", Path: "/local"}}, nil).Maybe()
	mockTmux.On("ListAllWindows").Return(map[string][]model.TmuxWindow(nil), nil).Maybe()
	mockTmuxinator := tmuxinator.NewMockTmuxinator(t)
	mockTmuxinator.On("List").Return([]*model.TmuxinatorConfig{}, nil).Maybe()
	mockZoxide := zoxide.NewMockZoxide(t)
	mockZoxide.On("ListResults").Return([]*model.ZoxideResult{}, nil).Maybe()
	runner := &sshJSONExec{responses: responses}
	return &Deps{
		BaseDeps: BaseDeps{Json: seshjson.NewJson(), Exec: runner},
		Config:   config,
		Lister:   lister.NewLister(config, home.NewMockHome(t), mockTmux, mockZoxide, mockTmuxinator, nil, nil, runner),
	}
}

type sshJSONExec struct {
	responses map[string]execwrap.CommandResult
}

func (e *sshJSONExec) LookPath(string) (string, error)            { panic("unexpected LookPath") }
func (e *sshJSONExec) Command(string, ...string) execwrap.ExecCmd { panic("unexpected Command") }
func (e *sshJSONExec) RunContext(_ context.Context, name string, args ...string) (execwrap.CommandResult, error) {
	if name != "ssh" || len(args) != 7 {
		panic("unexpected discovery command")
	}
	result, ok := e.responses[args[5]]
	if !ok {
		panic("unexpected host probe")
	}
	if result.ExitCode != 0 {
		return result, fmt.Errorf("exit status %d", result.ExitCode)
	}
	return result, nil
}
