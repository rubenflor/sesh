package lister

import (
	"context"
	"reflect"
	"testing"

	"github.com/joshmedeski/sesh/v2/execwrap"
)

type probeExec struct {
	result execwrap.CommandResult
	err    error
	name   string
	args   []string
}

func (e *probeExec) LookPath(string) (string, error) { panic("unexpected LookPath") }

func (e *probeExec) Command(string, ...string) execwrap.ExecCmd {
	panic("unexpected Command")
}

func (e *probeExec) RunContext(_ context.Context, name string, args ...string) (execwrap.CommandResult, error) {
	e.name = name
	e.args = append([]string(nil), args...)
	return e.result, e.err
}

func TestProbeSSHHostBuildsCanonicalSessionsAndSafeDiscoveryArgs(t *testing.T) {
	runner := &probeExec{result: execwrap.CommandResult{
		Stdout:   []byte("sesh-ssh-inventory-v1\n0\nnutiliti/2345\n"),
		ExitCode: 0,
	}}

	sessions, err := probeSSHHost(context.Background(), runner, "-inventory")
	if err != nil {
		t.Fatalf("probeSSHHost() error = %v", err)
	}
	if runner.name != "ssh" {
		t.Errorf("command = %q, want ssh", runner.name)
	}
	wantArgs := []string{"-T", "-n", "-o", "BatchMode=yes", "--", "-inventory", sshDiscoveryCommand}
	if !reflect.DeepEqual(runner.args, wantArgs) {
		t.Errorf("ssh args = %#v, want %#v", runner.args, wantArgs)
	}
	if got, want := sessions.OrderedIndex, []string{"ssh:ssh/-inventory/0", "ssh:ssh/-inventory/nutiliti%2F2345"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("OrderedIndex = %#v, want %#v", got, want)
	}
	for i, wantName := range []string{"ssh/-inventory/0", "ssh/-inventory/nutiliti%2F2345"} {
		got := sessions.Directory[sessions.OrderedIndex[i]]
		if got.Src != "ssh" || got.Name != wantName || got.Path != "" {
			t.Errorf("session[%d] = %+v, want Src=ssh Name=%q and empty Path", i, got, wantName)
		}
	}
}

func TestProbeSSHHostAcceptsSuccessfulStderrWarning(t *testing.T) {
	runner := &probeExec{result: execwrap.CommandResult{
		Stdout:   []byte("sesh-ssh-inventory-v1\nmain\n"),
		Stderr:   []byte("warning from remote shell\n"),
		ExitCode: 0,
	}}

	sessions, err := probeSSHHost(context.Background(), runner, "server")
	if err != nil {
		t.Fatalf("probeSSHHost() error = %v, want success despite stderr", err)
	}
	if len(sessions.OrderedIndex) != 1 {
		t.Fatalf("sessions = %#v, want one session", sessions.OrderedIndex)
	}
}

func TestProbeSSHHostAcceptsSuccessfulEmptyInventory(t *testing.T) {
	runner := &probeExec{result: execwrap.CommandResult{
		Stdout:   []byte(sshInventoryHeader),
		Stderr:   []byte("localized diagnostic from tmux"),
		ExitCode: 0,
	}}

	sessions, err := probeSSHHost(context.Background(), runner, "server")
	if err != nil {
		t.Fatalf("probeSSHHost() error = %v, want successful empty inventory", err)
	}
	if len(sessions.OrderedIndex) != 0 || len(sessions.Directory) != 0 {
		t.Fatalf("sessions = %#v, want empty inventory", sessions)
	}
	if runner.args[6] != sshDiscoveryCommand {
		t.Fatalf("remote command = %q, want framed empty-inventory contract", runner.args[6])
	}
}

func TestProbeSSHHostRejectsGenuineSSHFailure(t *testing.T) {
	runner := &probeExec{
		result: execwrap.CommandResult{Stderr: []byte("connection refused"), ExitCode: 255},
	}

	if _, err := probeSSHHost(context.Background(), runner, "server"); err == nil {
		t.Fatal("probeSSHHost() error = nil, want SSH failure")
	}
}

func TestProbeSSHHostRejectsMalformedTmuxOutput(t *testing.T) {
	runner := &probeExec{result: execwrap.CommandResult{
		Stdout:   []byte("sesh-ssh-inventory-v1\nmain\n\n"),
		ExitCode: 0,
	}}

	if _, err := probeSSHHost(context.Background(), runner, "server"); err == nil {
		t.Fatal("probeSSHHost() error = nil, want malformed output error")
	}
}

func TestParseTmuxSessionNames(t *testing.T) {
	output := sshInventoryHeader + "line\\nbreak\nliteral\\\\n\nslash\\\\\n\\001\\033\\177\ncafé/100% 'quotes' $();\n"
	names, err := parseTmuxSessionNames([]byte(output))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"line\nbreak", `literal\n`, `slash\`, "\x01\x1b\x7f", "café/100% 'quotes' $();"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %#v, want %#v", names, want)
	}
	for _, inventory := range []string{
		"", "main\n", sshInventoryHeader + "main", sshInventoryHeader + "\n",
		sshInventoryHeader + "bad\\\n", sshInventoryHeader + "bad\\q\n",
		sshInventoryHeader + "bad\\12\n", sshInventoryHeader + "bad\\400\n",
		sshInventoryHeader + "bad\\078\n", sshInventoryHeader + "bad\\000\n",
		sshInventoryHeader + "bad\x00\n", sshInventoryHeader + "bad\t\n",
	} {
		if _, err := parseTmuxSessionNames([]byte(inventory)); err == nil {
			t.Errorf("accepted malformed inventory %q", inventory)
		}
	}
}
