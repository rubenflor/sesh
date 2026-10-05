package tmux

import (
	"errors"
	"testing"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/oswrap"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/stretchr/testify/assert"
	mock "github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// stubNoClients makes ResolveClient come up empty, so SwitchOrAttach falls back
// to a bare `switch-client -t` and lets tmux pick the client itself.
func stubNoClients(mockOs *oswrap.MockOs, mockShell *shell.MockShell, bin string) {
	mockOs.On("Getenv", "SESH_CLIENT").Return("")
	mockOs.On("Getenv", "TMUX_PANE").Return("")
	mockOs.On("Getenv", "TMUX").Return("").Maybe()
	mockShell.On("ListCmd", bin, "list-clients", "-F", clientFormat).Return([]string{""}, nil)
}

func TestSwitchOrAttach(t *testing.T) {
	mockOs := new(oswrap.MockOs)
	mockShell := new(shell.MockShell)
	tmux := NewTmux(mockOs, mockShell, "")

	t.Run("switches because of option", func(t *testing.T) {
		mockOs.ExpectedCalls = nil
		mockShell.ExpectedCalls = nil
		stubNoClients(mockOs, mockShell, "tmux")
		mockShell.On("Cmd", "tmux", "switch-client", "-t", mock.Anything).Return("", nil)
		response, error := tmux.SwitchOrAttach("dotfiles", model.ConnectOpts{Switch: true})
		assert.Equal(t, "switching to tmux session: dotfiles", response)
		assert.Equal(t, nil, error)
	})

	t.Run("switches when attached", func(t *testing.T) {
		mockOs.ExpectedCalls = nil
		mockShell.ExpectedCalls = nil
		mockOs.On("Getenv", "TMUX").Return("/private/tmp/tmux-501/default,72439,4")
		stubNoClients(mockOs, mockShell, "tmux")
		mockShell.On("Cmd", "tmux", "switch-client", "-t", mock.Anything).Return("", nil)
		response, error := tmux.SwitchOrAttach("dotfiles", model.ConnectOpts{Switch: false})
		assert.Equal(t, "switching to tmux session: dotfiles", response)
		assert.Equal(t, nil, error)
	})

	t.Run("errors when switching to a missing session", func(t *testing.T) {
		mockOs.ExpectedCalls = nil
		mockShell.ExpectedCalls = nil
		mockOs.On("Getenv", "TMUX").Return("/private/tmp/tmux-501/default,72439,4")
		stubNoClients(mockOs, mockShell, "tmux")
		mockShell.On("Cmd", "tmux", "switch-client", "-t", mock.Anything).Return("", errors.New("can't find session: dotfiles"))
		response, err := tmux.SwitchOrAttach("dotfiles", model.ConnectOpts{Switch: false})
		assert.Equal(t, "", response)
		assert.EqualError(t, err, "failed to switch to tmux session: can't find session: dotfiles")
	})

	t.Run("attaches", func(t *testing.T) {
		mockOs.ExpectedCalls = nil
		mockShell.ExpectedCalls = nil
		mockOs.On("Getenv", "TMUX").Return("")
		mockShell.On("CmdWithOutput", "tmux", "attach-session", "-t", mock.Anything).Return("", nil)
		response, error := tmux.SwitchOrAttach("dotfiles", model.ConnectOpts{Switch: false})
		assert.Equal(t, "attaching to tmux session: dotfiles", response)
		assert.Equal(t, nil, error)
	})
}

func TestCustomBin(t *testing.T) {
	mockOs := new(oswrap.MockOs)
	mockShell := new(shell.MockShell)
	psmux := NewTmux(mockOs, mockShell, "psmux")

	t.Run("uses psmux binary for new session", func(t *testing.T) {
		mockShell.On("Cmd", "psmux", "new-session", "-d", "-s", "dotfiles", "-c", "/home/user/dotfiles").Return("", nil)
		_, err := psmux.NewSession("dotfiles", "/home/user/dotfiles")
		assert.Nil(t, err)
		mockShell.AssertCalled(t, "Cmd", "psmux", "new-session", "-d", "-s", "dotfiles", "-c", "/home/user/dotfiles")
	})

	t.Run("uses psmux binary for switch client", func(t *testing.T) {
		mockOs.On("Getenv", "TMUX").Return("/private/tmp/tmux-501/default,72439,4")
		stubNoClients(mockOs, mockShell, "psmux")
		mockShell.On("Cmd", "psmux", "switch-client", "-t", "dotfiles").Return("", nil)
		response, err := psmux.SwitchOrAttach("dotfiles", model.ConnectOpts{Switch: true})
		assert.Nil(t, err)
		assert.Equal(t, "switching to tmux session: dotfiles", response)
		mockShell.AssertCalled(t, "Cmd", "psmux", "switch-client", "-t", "dotfiles")
	})

	t.Run("uses psmux binary for attach session", func(t *testing.T) {
		mockOs.ExpectedCalls = nil
		mockShell.ExpectedCalls = nil
		mockOs.On("Getenv", "TMUX").Return("")
		mockShell.On("CmdWithOutput", "psmux", "attach-session", "-t", "dotfiles").Return("", nil)
		response, err := psmux.SwitchOrAttach("dotfiles", model.ConnectOpts{Switch: false})
		assert.Nil(t, err)
		assert.Equal(t, "attaching to tmux session: dotfiles", response)
		mockShell.AssertCalled(t, "CmdWithOutput", "psmux", "attach-session", "-t", "dotfiles")
	})
}

func TestSwitchOrAttachSSHBridge(t *testing.T) {
	for _, tt := range []struct {
		name     string
		switchTo bool
		attached bool
	}{
		{"plain outside tmux attaches", false, false},
		{"plain inside tmux switches", false, true},
		{"external switch targets attached client", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const bridge = "sesh-ssh-71733fb26ad99c67e0bf6bbe-0"
			mOs := oswrap.NewMockOs(t)
			mShell := shell.NewMockShell(t)
			want := "switching to tmux session: " + bridge
			if tt.switchTo {
				mOs.EXPECT().Getenv("SESH_CLIENT").Return("/dev/ttys002").Once()
				mShell.EXPECT().Cmd("tmux", "switch-client", "-c", "/dev/ttys002", "-t", bridge).Return("", nil).Once()
			} else if tt.attached {
				mOs.EXPECT().Getenv("TMUX").Return("/tmp/tmux-501/default,72439,4").Twice()
				mOs.EXPECT().Getenv("SESH_CLIENT").Return("").Once()
				mOs.EXPECT().Getenv("TMUX_PANE").Return("%1").Once()
				mShell.EXPECT().Cmd("tmux", "switch-client", "-t", bridge).Return("", nil).Once()
			} else {
				mOs.EXPECT().Getenv("TMUX").Return("").Once()
				mShell.EXPECT().CmdWithOutput("tmux", "attach-session", "-t", bridge).Return("", nil).Once()
				want = "attaching to tmux session: " + bridge
			}
			tm := NewTmux(mOs, mShell, "")

			response, err := tm.SwitchOrAttach(bridge, model.ConnectOpts{Switch: tt.switchTo})

			require.NoError(t, err)
			assert.Equal(t, want, response)
		})
	}
}
