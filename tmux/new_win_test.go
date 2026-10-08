package tmux

import (
	"testing"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewWindowInSession(t *testing.T) {
	const format = "#{session_name}:#{window_index}"

	t.Run("targets the session unambiguously with a trailing colon", func(t *testing.T) {
		mockShell := &shell.MockShell{}
		tmux := &RealTmux{shell: mockShell, bin: "tmux"}
		mockShell.EXPECT().
			Cmd("tmux", "new-window", "-P", "-F", format, "-n", "agent", "-c", "/home/dev/project", "-t", "project:").
			Return("project:2\n", nil)

		result, err := tmux.NewWindowInSession(model.TmuxWindowOpts{
			Name: "agent", StartDir: "/home/dev/project", TargetSession: "project",
		})

		assert.Nil(t, err)
		assert.Equal(t, "project:2", result)
	})

	t.Run("omits the target when no session is provided", func(t *testing.T) {
		mockShell := &shell.MockShell{}
		tmux := &RealTmux{shell: mockShell, bin: "tmux"}
		mockShell.EXPECT().
			Cmd("tmux", "new-window", "-P", "-F", format, "-n", "agent", "-c", "/home/dev/project").
			Return("", nil)

		result, err := tmux.NewWindowInSession(model.TmuxWindowOpts{
			Name: "agent", StartDir: "/home/dev/project",
		})

		assert.Nil(t, err)
		assert.Equal(t, "", result)
	})

	t.Run("passes the command after -- so it becomes the pane process", func(t *testing.T) {
		mockShell := &shell.MockShell{}
		tmux := &RealTmux{shell: mockShell, bin: "tmux"}
		mockShell.EXPECT().
			Cmd("tmux", "new-window", "-P", "-F", format, "-c", "/home/dev/project", "-d", "-t", "project:", "--", `claude "do the thing"`).
			Return("project:3", nil)

		result, err := tmux.NewWindowInSession(model.TmuxWindowOpts{
			StartDir: "/home/dev/project", TargetSession: "project",
			Command: `claude "do the thing"`, Background: true,
		})

		assert.Nil(t, err)
		assert.Equal(t, "project:3", result)
	})

	t.Run("uses the configured tmux binary", func(t *testing.T) {
		mockShell := &shell.MockShell{}
		tmux := &RealTmux{shell: mockShell, bin: "/opt/bin/tmux"}
		mockShell.EXPECT().
			Cmd("/opt/bin/tmux", "new-window", "-P", "-F", format).
			Return("", nil)

		_, err := tmux.NewWindowInSession(model.TmuxWindowOpts{})

		assert.Nil(t, err)
	})
}

func TestNewWindowPaneInSession(t *testing.T) {
	const format = "#{session_id} #{window_id} #{pane_id} #{pid}:#{start_time}:#{socket_path}"
	const generation = "101:1700000000:/tmp/socket with spaces::fixture"
	opts := model.TmuxWindowOpts{Name: "editor", StartDir: "/work", TargetSession: "$1"}
	for _, verified := range []bool{false, true} {
		t.Run(map[bool]string{false: "creation binds generation", true: "preserves supplied generation"}[verified], func(t *testing.T) {
			s := shell.NewMockShell(t)
			tm := NewTmux(nil, s, "custom-tmux")
			var pane model.TmuxPane
			var err error
			if verified {
				const condition = "#{&&:#{==:#{session_id},$1},#{==:#{pid}:#{start_time}:#{socket_path},#{l:101:1700000000:/tmp/socket with spaces::fixture}}}"
				s.EXPECT().CmdCapture("custom-tmux", "-u", "if-shell", "-F", "-t", "$1:", condition,
					"'new-window' '-P' '-F' '"+format+"' '-n' 'editor' '-c' '/work' '-t' '$1:'", "if-shell").Return("$1 @7 %6 "+generation, nil).Once()
				pane, err = tm.NewWindowPaneInSession(opts, generation)
			} else {
				s.EXPECT().CmdCapture("custom-tmux", "-u", "new-window", "-P", "-F", format, "-n", "editor", "-c", "/work", "-t", "$1:").Return("$1 @7 %6 "+generation, nil).Once()
				pane, err = tm.NewWindowPaneInSession(opts)
			}
			require.NoError(t, err)
			require.Equal(t, model.TmuxPane{SessionID: "$1", WindowID: "@7", PaneID: "%6", ServerGeneration: generation}, pane)
		})
	}
}

func TestNewWindowPaneRejectsUnverifiableCreation(t *testing.T) {
	const generation = "101:1700000000:/tmp/socket"
	for _, output := range []string{
		"", "display:4", "$1 @7 %6", "$1 @7 %6 ",
		"$01 @7 %6 " + generation, "$2 @7 %6 " + generation,
		"$1 @ %6 " + generation, "$1 @07 %6 " + generation,
		"$1 @name %6 " + generation, "$1 @-7 %6 " + generation,
		"$1 @18446744073709551616 %6 " + generation,
		"$1 @7 % " + generation, "$1 @7 %06 " + generation,
		"$1 @7 %name " + generation, "$1 @7 %-6 " + generation,
		"$1 @7 %18446744073709551616 " + generation,
		"$1 @7 %6 unknown", "$1 @7 %6 101:0:/tmp/socket",
		"$1 @7 %6 #{pid}:#{start_time}:#{socket_path}",
		" $1 @7 %6 " + generation, "$1  @7 %6 " + generation,
		"$1 @7 %6 " + generation + "\n$1 @8 %2 " + generation,
	} {
		t.Run(output, func(t *testing.T) {
			s := shell.NewMockShell(t)
			s.EXPECT().CmdCapture("custom-tmux", "-u", "new-window", "-P", "-F", createdPaneFormat, "-t", "$1:").Return(output, nil).Once()
			pane, err := NewTmux(nil, s, "custom-tmux").NewWindowPaneInSession(model.TmuxWindowOpts{TargetSession: "$1"})
			require.Error(t, err)
			require.Equal(t, model.TmuxPane{}, pane)
		})
	}
	for _, target := range []string{"", "display", "$01", "$-1"} {
		t.Run("invalid session "+target, func(t *testing.T) {
			pane, err := NewTmux(nil, shell.NewMockShell(t), "custom-tmux").NewWindowPaneInSession(model.TmuxWindowOpts{TargetSession: target})
			require.Error(t, err)
			require.Equal(t, model.TmuxPane{}, pane)
		})
	}
}

func TestNewWindowPaneCreationFailure(t *testing.T) {
	s := shell.NewMockShell(t)
	s.EXPECT().CmdCapture("custom-tmux", "-u", "new-window", "-P", "-F", createdPaneFormat, "-t", "$1:").Return("partial output", assert.AnError).Once()
	pane, err := NewTmux(nil, s, "custom-tmux").NewWindowPaneInSession(model.TmuxWindowOpts{TargetSession: "$1"})
	require.ErrorIs(t, err, assert.AnError)
	require.Equal(t, model.TmuxPane{}, pane)
}

func TestNewWindowPaneRejectsChangedGeneration(t *testing.T) {
	const generation = "101:1700000000:/tmp/socket"
	const condition = "#{&&:#{==:#{session_id},$1},#{==:#{pid}:#{start_time}:#{socket_path},#{l:101:1700000000:/tmp/socket}}}"
	s := shell.NewMockShell(t)
	s.EXPECT().CmdCapture("custom-tmux", "-u", "if-shell", "-F", "-t", "$1:", condition,
		"'new-window' '-P' '-F' '"+createdPaneFormat+"' '-t' '$1:'", "if-shell").Return("$1 @7 %6 102:1700000001:/tmp/socket", nil).Once()
	pane, err := NewTmux(nil, s, "custom-tmux").NewWindowPaneInSession(model.TmuxWindowOpts{TargetSession: "$1"}, generation)
	require.Error(t, err)
	require.Equal(t, model.TmuxPane{}, pane)
}

func TestNextWindowInSession(t *testing.T) {
	t.Run("targets the session unambiguously with a trailing colon", func(t *testing.T) {
		mockShell := &shell.MockShell{}
		tmux := &RealTmux{shell: mockShell, bin: "tmux"}
		mockShell.EXPECT().
			Cmd("tmux", "next-window", "-t", "project:").
			Return("", nil)

		result, err := tmux.NextWindowInSession("project")

		assert.Nil(t, err)
		assert.Equal(t, "", result)
	})
}
