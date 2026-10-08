package tmux

import (
	"testing"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/oswrap"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifiedSessionCommandsCarryGeneration(t *testing.T) {
	const generation = "101:1700000000:/tmp/socket,#[x]#{x}#}::fixture"
	const condition = "#{&&:#{==:#{session_id},$1},#{==:#{pid}:#{start_time}:#{socket_path},#{l:101:1700000000:/tmp/socket#,##[x]##{x#}###}::fixture}}}"
	for _, operation := range []struct {
		name, bin, command string
		run                func(*RealTmux) error
	}{
		{"capture", "custom-tmux", "'capture-pane' '-e' '-p' '-t' '$1'", func(tm *RealTmux) error { _, err := tm.CapturePane("$1", generation); return err }},
		{"rename", "custom-tmux", "'rename-session' '-t' '$1' 'new name'", func(tm *RealTmux) error { _, err := tm.RenameSession("$1", "new name", generation); return err }},
		{"create window", "custom-tmux", "'new-window' '-P' '-F' '#{session_name}:#{window_index}' '-c' '/original' '-t' '$1:'", func(tm *RealTmux) error {
			_, err := tm.NewWindowInSession(model.TmuxWindowOpts{TargetSession: "$1", StartDir: "/original"}, generation)
			return err
		}},
		{"send keys", "custom-tmux", "'send-keys' '-t' '$1:4' 'command' 'Enter'", func(tm *RealTmux) error { _, err := tm.SendKeys("$1:4", "command", generation); return err }},
		{"select window", "custom-tmux", "'select-window' '-t' '$1:4'", func(tm *RealTmux) error { _, err := tm.SelectWindow("$1:4", generation); return err }},
		{"switch", "custom-tmux", "'switch-client' '-t' '$1'", func(tm *RealTmux) error { _, err := tm.SwitchClient("$1", generation); return err }},
		{"switch client", "custom-tmux", "'switch-client' '-c' 'client' '-t' '$1'", func(tm *RealTmux) error { _, err := tm.SwitchClientTarget("client", "$1", generation); return err }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			for _, commandErr := range []error{nil, assert.AnError} {
				s := shell.NewMockShell(t)
				s.EXPECT().CmdCapture(operation.bin, "if-shell", "-F", "-t", "$1:", condition, operation.command, "if-shell").Return("", commandErr).Once()
				tm := &RealTmux{shell: s, bin: "custom-tmux"}
				require.ErrorIs(t, operation.run(tm), commandErr)
			}
		})
	}
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "list windows", true: "attach"}[interactive], func(t *testing.T) {
			s := shell.NewMockShell(t)
			tm := &RealTmux{shell: s, bin: "custom-tmux"}
			if interactive {
				s.EXPECT().CmdWithOutput("custom-tmux", "if-shell", "-F", "-t", "$1:", condition, "'attach-session' '-t' '$1'", "if-shell").Return("", assert.AnError).Once()
				_, err := tm.AttachSession("$1", generation)
				require.ErrorIs(t, err, assert.AnError)
			} else {
				s.EXPECT().ListCmd("custom-tmux", "-u", "if-shell", "-F", "-t", "$1:", condition,
					"'list-windows' '-t' '$1' '-F' '"+testWindowInventoryFormat+"'", "if-shell").Return([]string{windowInventoryRow(model.TmuxWindow{ID: "@7", ServerGeneration: generation, Index: 4, Name: "notes", Path: "/original", Active: true})}, nil).Once()
				windows, err := tm.ListWindows("$1", generation)
				require.NoError(t, err)
				require.Equal(t, []*model.TmuxWindow{{ID: "@7", ServerGeneration: generation, Index: 4, Name: "notes", Path: "/original", Active: true}}, windows)
			}
		})
	}
}

func TestSwitchOrAttachPreservesGeneration(t *testing.T) {
	const generation = "101:1700000000:/tmp/socket"
	const condition = "#{&&:#{==:#{session_id},$1},#{==:#{pid}:#{start_time}:#{socket_path},#{l:101:1700000000:/tmp/socket}}}"
	for _, inside := range []bool{false, true} {
		t.Run(map[bool]string{false: "attach", true: "switch"}[inside], func(t *testing.T) {
			s := shell.NewMockShell(t)
			o := oswrap.NewMockOs(t)
			tm := &RealTmux{os: o, shell: s, bin: "custom-tmux"}
			if inside {
				o.EXPECT().Getenv("TMUX").Return("/tmp/socket,101,1").Once()
				o.EXPECT().Getenv("SESH_CLIENT").Return("client").Once()
				s.EXPECT().CmdCapture("custom-tmux", "if-shell", "-F", "-t", "$1:", condition,
					"'switch-client' '-c' 'client' '-t' '$1'", "if-shell").Return("", nil).Once()
			} else {
				o.EXPECT().Getenv("TMUX").Return("").Once()
				s.EXPECT().CmdWithOutput("custom-tmux", "if-shell", "-F", "-t", "$1:", condition,
					"'attach-session' '-t' '$1'", "if-shell").Return("", nil).Once()
			}
			_, err := tm.SwitchOrAttach("$1", model.ConnectOpts{}, generation)
			require.NoError(t, err)
		})
	}
}

func TestSessionGenerationCommandsRejectUnknownIdentity(t *testing.T) {
	for _, identity := range []struct{ id, generation string }{
		{"", "101:1700000000:/tmp/socket"}, {"name", "101:1700000000:/tmp/socket"},
		{"$01", "101:1700000000:/tmp/socket"}, {"$1", ""}, {"$1", "101:0:/tmp/socket"},
	} {
		t.Run(identity.id+"@"+identity.generation, func(t *testing.T) {
			s := shell.NewMockShell(t)
			tm := &RealTmux{shell: s, bin: "custom-tmux"}
			_, err := VerifyLocalSession(tm, identity.id, identity.generation)
			require.Error(t, err)
			_, err = tm.RenameSession(identity.id, "new", identity.generation)
			require.Error(t, err)
		})
	}
}

func TestVerifiedWindowCommandsCarryIdentity(t *testing.T) {
	const generation = "101:1700000000:/tmp/socket"
	const condition = "#{&&:#{&&:#{==:#{session_id},$1},#{==:#{pid}:#{start_time}:#{socket_path},#{l:101:1700000000:/tmp/socket}}},#{==:#{window_id},@7}}"
	for _, operation := range []struct {
		name, bin, command string
		interactive        bool
		run                func(*RealTmux) error
	}{
		{"input", "custom-tmux", "'send-keys' '-t' '$1:@7' 'command' 'Enter'", false, func(tm *RealTmux) error { _, err := tm.SendKeys("$1:@7", "command", generation); return err }},
		{"selection", "custom-tmux", "'select-window' '-t' '$1:@7'", false, func(tm *RealTmux) error { _, err := tm.SelectWindow("$1:@7", generation); return err }},
		{"background verification", "custom-tmux", "'capture-pane' '-e' '-p' '-t' '$1:@7'", false, func(tm *RealTmux) error { _, err := tm.CapturePane("$1:@7", generation); return err }},
		{"switch", "custom-tmux", "'switch-client' '-t' '$1:@7'", false, func(tm *RealTmux) error { _, err := tm.SwitchClient("$1:@7", generation); return err }},
		{"explicit client", "custom-tmux", "'switch-client' '-c' 'client' '-t' '$1:@7'", false, func(tm *RealTmux) error { _, err := tm.SwitchClientTarget("client", "$1:@7", generation); return err }},
		{"attach", "custom-tmux", "'attach-session' '-t' '$1:@7'", true, func(tm *RealTmux) error { _, err := tm.AttachSession("$1:@7", generation); return err }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			for _, commandErr := range []error{nil, assert.AnError} {
				s := shell.NewMockShell(t)
				tm := &RealTmux{shell: s, bin: "custom-tmux"}
				if operation.interactive {
					s.EXPECT().CmdWithOutput(operation.bin, "if-shell", "-F", "-t", "$1:@7", condition, operation.command, "if-shell").Return("", commandErr).Once()
				} else {
					s.EXPECT().CmdCapture(operation.bin, "if-shell", "-F", "-t", "$1:@7", condition, operation.command, "if-shell").Return("", commandErr).Once()
				}
				require.ErrorIs(t, operation.run(tm), commandErr)
			}
		})
	}
}

func TestVerifiedWindowCommandsRejectUnknownIdentity(t *testing.T) {
	for _, identity := range []struct{ target, generation string }{
		{"$1:@", "101:1700000000:/tmp/socket"},
		{"$1:@01", "101:1700000000:/tmp/socket"},
		{"$1:@name", "101:1700000000:/tmp/socket"},
		{"$1:@-1", "101:1700000000:/tmp/socket"},
		{"$1:@7.0", "101:1700000000:/tmp/socket"},
		{"$1:@18446744073709551616", "101:1700000000:/tmp/socket"},
		{"@7", "101:1700000000:/tmp/socket"},
		{"$01:@7", "101:1700000000:/tmp/socket"},
		{"$1:@7", ""}, {"$1:@7", "unknown"},
	} {
		t.Run(identity.target+"/"+identity.generation, func(t *testing.T) {
			tm := &RealTmux{shell: shell.NewMockShell(t), bin: "custom-tmux"}
			_, err := tm.SendKeys(identity.target, "command", identity.generation)
			require.Error(t, err)
			_, err = tm.SelectWindow(identity.target, identity.generation)
			require.Error(t, err)
			_, err = tm.AttachSession(identity.target, identity.generation)
			require.Error(t, err)
		})
	}
}

func TestCreatedPaneSendKeysCarriesFullIdentity(t *testing.T) {
	const generation = "101:1700000000:/tmp/socket"
	const condition = "#{&&:#{&&:#{&&:#{==:#{session_id},$1},#{==:#{pid}:#{start_time}:#{socket_path},#{l:101:1700000000:/tmp/socket}}},#{==:#{window_id},@7}},#{==:#{pane_id},%6}}"
	for _, commandErr := range []error{nil, assert.AnError} {
		s := shell.NewMockShell(t)
		s.EXPECT().CmdCapture("custom-tmux", "if-shell", "-F", "-t", "$1:@7.%6", condition,
			"'send-keys' '-t' '$1:@7.%6' 'script' 'Enter'", "if-shell").Return("", commandErr).Once()
		_, err := NewTmux(nil, s, "custom-tmux").SendKeys("$1:@7.%6", "script", generation)
		require.ErrorIs(t, err, commandErr)
	}
}

func TestCreatedPaneSendKeysRejectsUnverifiableIdentity(t *testing.T) {
	for _, target := range []string{
		"@7.%6", "$01:@7.%6", "$1:@07.%6", "$1:@7.", "$1:@7.0",
		"$1:@7.%", "$1:@7.%06", "$1:@7.%name", "$1:@7.%-6",
		"$1:@7.%6.extra", "$1:@7.%18446744073709551616",
	} {
		t.Run(target, func(t *testing.T) {
			_, err := NewTmux(nil, shell.NewMockShell(t), "custom-tmux").SendKeys(target, "script", "101:1700000000:/tmp/socket")
			require.Error(t, err)
		})
	}
	for _, generation := range []string{"", "unknown", "101:0:/tmp/socket"} {
		_, err := NewTmux(nil, shell.NewMockShell(t), "custom-tmux").SendKeys("$1:@7.%6", "script", generation)
		require.Error(t, err)
	}
}
