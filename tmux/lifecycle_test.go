package tmux

import (
	"errors"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestSessionOptionsBeforeInitialProcess(t *testing.T) {
	s := shell.NewMockShell(t)
	var gate string
	create := s.EXPECT().CmdCapture("tmux", "new-session", "-d", "-s", "worker", "-e", "MARK=identity", "-P", "-F", "#{session_id} #{pane_id}", "--", gatedWorkerCommand(&gate)).Return("$4 %9", nil).Once()
	status := s.EXPECT().CmdCapture("tmux", "set-option", "-t", "$4", "status", "off").Return("", nil).Once().NotBefore(create)
	remain := s.EXPECT().CmdCapture("tmux", "set-option", "-p", "-t", "%9", "remain-on-exit", "failed").Return("", nil).Once().NotBefore(status)
	s.EXPECT().CmdCapture("tmux", "wait-for", "-S", mock.MatchedBy(func(value string) bool { return gate != "" && value == gate })).Return("", nil).Once().NotBefore(remain)
	tm := NewTmux(nil, s, "")
	_, err := tm.NewSessionWithCommand(model.TmuxSessionOpts{
		Name: "worker", Command: "exec worker", Environment: []string{"MARK=identity"},
		SessionOptions: []model.TmuxOption{{Name: "status", Value: "off"}},
		PaneOptions:    []model.TmuxOption{{Name: "remain-on-exit", Value: "failed"}},
	})
	require.NoError(t, err)
}

func gatedWorkerCommand(gate *string) interface{} {
	return mock.MatchedBy(func(command string) bool {
		value, ok := strings.CutPrefix(command, "'tmux' wait-for '")
		if !ok {
			return false
		}
		value, ok = strings.CutSuffix(value, "' && exec worker")
		if !ok || !strings.HasPrefix(value, "sesh-start-") {
			return false
		}
		*gate = value
		return true
	})
}

func TestSessionOptionsFailureNeverStartsCommand(t *testing.T) {
	for _, stage := range []string{"session option", "pane option", "start"} {
		t.Run(stage, func(t *testing.T) {
			s := shell.NewMockShell(t)
			failure := errors.New("unsupported capability")
			var gate string
			s.EXPECT().CmdCapture("tmux", "new-session", "-d", "-s", "worker", "-P", "-F", "#{session_id} #{pane_id}", "--", gatedWorkerCommand(&gate)).Return("$4 %9", nil).Once()
			var sessionErr, paneErr error
			if stage == "session option" {
				sessionErr = failure
			}
			if stage == "pane option" {
				paneErr = failure
			}
			s.EXPECT().CmdCapture("tmux", "set-option", "-t", "$4", "status", "off").Return("", sessionErr).Once()
			if sessionErr == nil {
				s.EXPECT().CmdCapture("tmux", "set-option", "-p", "-t", "%9", "remain-on-exit", "failed").Return("", paneErr).Once()
			}
			if stage == "start" {
				s.EXPECT().CmdCapture("tmux", "wait-for", "-S", mock.MatchedBy(func(value string) bool { return gate != "" && value == gate })).Return("", failure).Once()
			}
			s.EXPECT().Cmd("tmux", "kill-session", "-t", "$4:").Return("", nil).Once()
			tm := NewTmux(nil, s, "")
			_, err := tm.NewSessionWithCommand(model.TmuxSessionOpts{
				Name: "worker", Command: "exec worker",
				SessionOptions: []model.TmuxOption{{Name: "status", Value: "off"}},
				PaneOptions:    []model.TmuxOption{{Name: "remain-on-exit", Value: "failed"}},
			})
			require.ErrorIs(t, err, failure)
			if stage != "start" {
				require.ErrorContains(t, err, "does not support required")
			}
		})
	}
}

func TestCreationOptionsRequireCommand(t *testing.T) {
	tm := NewTmux(nil, shell.NewMockShell(t), "")
	_, err := tm.NewSessionWithCommand(model.TmuxSessionOpts{Name: "worker", PaneOptions: []model.TmuxOption{{Name: "remain-on-exit", Value: "failed"}}})
	require.ErrorContains(t, err, "require an initial command")
}

func TestUnreadableCreationIDsCleanUpExactCreatedSession(t *testing.T) {
	s := shell.NewMockShell(t)
	var gate string
	s.EXPECT().CmdCapture("tmux", "new-session", "-d", "-s", "worker", "-P", "-F", "#{session_id} #{pane_id}", "--", gatedWorkerCommand(&gate)).Return("unsupported format", nil).Once()
	s.EXPECT().Cmd("tmux", "kill-session", "-t", "=worker:").Return("", nil).Once()
	_, err := NewTmux(nil, s, "").NewSessionWithCommand(model.TmuxSessionOpts{
		Name: "worker", Command: "exec worker", SessionOptions: []model.TmuxOption{{Name: "status", Value: "off"}},
	})
	require.ErrorContains(t, err, "unreadable session/pane IDs")
}

func TestIsPaneDead(t *testing.T) {
	for _, state := range []string{"0", "1", "", "unknown"} {
		t.Run(state, func(t *testing.T) {
			s := shell.NewMockShell(t)
			s.EXPECT().CmdCapture("custom-tmux", "display-message", "-p", "-t", "$8:", "#{pane_dead}").Return(state, nil).Once()
			dead, err := NewTmux(nil, s, "custom-tmux").IsPaneDead("$8")
			if state == "0" || state == "1" {
				require.NoError(t, err)
				require.Equal(t, state == "1", dead)
			} else {
				require.ErrorContains(t, err, "unreadable pane state")
			}
		})
	}
}
