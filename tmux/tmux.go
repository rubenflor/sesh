package tmux

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/oswrap"
	"github.com/joshmedeski/sesh/v2/shell"
)

type Tmux interface {
	ListSessions() ([]*model.TmuxSession, error)
	ListWindows(targetSession string) ([]*model.TmuxWindow, error)
	ListAllWindowNames(format string) (map[string][]string, error)
	ListAllWindows() (map[string][]model.TmuxWindow, error)
	NewSession(sessionName string, startDir string) (string, error)
	// With creation options, the result is "<session_id> <pane_id>" for safe
	// handoff to later operations. Without options, it is the command's stdout.
	NewSessionWithCommand(opts model.TmuxSessionOpts) (string, error)
	GetSessionEnvironment(targetSession string, name string) (string, error)
	IsPaneDead(targetSession string) (bool, error)
	NewWindowInSession(opts model.TmuxWindowOpts) (string, error)
	IsAttached() bool
	AttachSession(targetSession string) (string, error)
	SendKeys(name string, command string) (string, error)
	SwitchClient(targetSession string) (string, error)
	CapturePane(targetSession string) (string, error)
	NextWindowInSession(targetSession string) (string, error)
	SelectWindow(targetWindow string) (string, error)
	SwitchOrAttach(name string, opts model.ConnectOpts) (string, error)
	ListTmuxPanes() ([]*model.TmuxPane, error)
	SelectPane(windowIndex int, paneIndex int) (string, error)
	GetCurrentSession() (string, error)
	ListClients() ([]model.TmuxClient, error)
	ResolveClient() string
	FocusedSessionPath() (string, error)
	SwitchClientTarget(client string, targetSession string) (string, error)
	RenameSession(target string, newName string) (string, error)
	KillSession(target string) (string, error)
	KillSessionInGeneration(sessionID, serverGeneration string) (string, error)
}

type RealTmux struct {
	os    oswrap.Os
	shell shell.Shell
	bin   string
}

func NewTmux(os oswrap.Os, shell shell.Shell, bin string) Tmux {
	if bin == "" {
		bin = "tmux"
	}
	return &RealTmux{os, shell, bin}
}

func (t *RealTmux) AttachSession(targetSession string) (string, error) {
	// Using t.shell.Cmd() here (capturing stdout/stderr in buffers) would block psmux on Windows.
	return t.shell.CmdWithOutput(t.bin, "attach-session", "-t", targetSession)
}

func (t *RealTmux) SwitchClient(targetSession string) (string, error) {
	return t.shell.Cmd(t.bin, "switch-client", "-t", targetSession)
}

func (t *RealTmux) SendKeys(targetPane string, keys string) (string, error) {
	return t.shell.Cmd(t.bin, "send-keys", "-t", targetPane, keys, "Enter")
}

func (t *RealTmux) NewSession(sessionName string, startDir string) (string, error) {
	return t.shell.Cmd(t.bin, "new-session", "-d", "-s", sessionName, "-c", startDir)
}

func (t *RealTmux) NewSessionWithCommand(opts model.TmuxSessionOpts) (string, error) {
	args := []string{"new-session", "-d", "-s", opts.Name}
	if opts.StartDir != "" {
		args = append(args, "-c", opts.StartDir)
	}
	for _, assignment := range opts.Environment {
		args = append(args, "-e", assignment)
	}
	if len(opts.SessionOptions) > 0 || len(opts.PaneOptions) > 0 {
		return t.newSessionWithOptions(args, opts)
	}
	if opts.Command != "" {
		args = append(args, "--", opts.Command)
	}
	return t.shell.Cmd(t.bin, args...)
}

// GetSessionEnvironment reads only the session environment, without falling
// back to global values. An unset or removed variable returns an empty string.
func (t *RealTmux) GetSessionEnvironment(targetSession string, name string) (string, error) {
	output, err := t.shell.CmdCapture(t.bin, "show-environment", "-t", targetSession, "--", name)
	if err != nil {
		var exitErr *exec.ExitError
		if output != "" || !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return "", err
		}
		// A missing variable exits 1. Confirm that the same session environment
		// is readable before treating this as absence; never parse other values.
		if _, probeErr := t.shell.CmdCapture(t.bin, "show-environment", "-t", targetSession); probeErr == nil {
			return "", nil
		}
		return "", err
	}
	if output == "" || output == "-"+name {
		return "", nil
	}
	value, found := strings.CutPrefix(output, name+"=")
	if !found {
		return "", fmt.Errorf("unreadable session environment variable %q", name)
	}
	return value, nil
}

func (t *RealTmux) CapturePane(targetSession string) (string, error) {
	return t.shell.Cmd(t.bin, "capture-pane", "-e", "-p", "-t", targetSession)
}

func (t *RealTmux) NextWindowInSession(targetSession string) (string, error) {
	// trailing colon forces session (not window) target resolution
	return t.shell.Cmd(t.bin, "next-window", "-t", targetSession+":")
}

func (t *RealTmux) IsAttached() bool {
	return len(t.os.Getenv("TMUX")) > 0
}

func (t *RealTmux) SwitchClientTarget(client string, targetSession string) (string, error) {
	return t.shell.Cmd(t.bin, "switch-client", "-c", client, "-t", targetSession)
}
