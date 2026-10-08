package tmux

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const testWindowInventoryFormat = "#{window_id}::#{window_index}::#{window_active}::#{n:window_name}::#{n:pane_current_path}::#{n:#{pid}:#{start_time}:#{socket_path}}::#{window_name}#{pane_current_path}#{pid}:#{start_time}:#{socket_path}"

func windowInventoryRow(window model.TmuxWindow) string {
	active := 0
	if window.Active {
		active = 1
	}
	return fmt.Sprintf("%s::%d::%d::%d::%d::%d::%s%s%s", window.ID, window.Index, active, len(window.Name), len(window.Path), len(window.ServerGeneration), window.Name, window.Path, window.ServerGeneration)
}

func TestListWindows(t *testing.T) {
	const generation = "101:1700000000:/tmp/socket::suffix"
	t.Run("returns parsed windows", func(t *testing.T) {
		mockShell := &shell.MockShell{}
		tmux := NewTmux(nil, mockShell, "")
		mockShell.EXPECT().ListCmd("tmux", "-u", "list-windows", "-F", testWindowInventoryFormat).Return(
			[]string{windowInventoryRow(model.TmuxWindow{ID: "@7", Index: 0, Name: "editor", Path: "/Users/josh/c/sesh", ServerGeneration: generation}), windowInventoryRow(model.TmuxWindow{ID: "@8", Index: 1, Name: "server", Path: "/Users/josh/c/sesh", Active: true, ServerGeneration: generation}), ""},
			nil,
		)
		windows, err := tmux.ListWindows("")
		assert.Nil(t, err)
		assert.Len(t, windows, 2)
		assert.Equal(t, "@7", windows[0].ID)
		assert.Equal(t, generation, windows[0].ServerGeneration)
		assert.Equal(t, "editor", windows[0].Name)
		assert.Equal(t, "/Users/josh/c/sesh", windows[0].Path)
		assert.Equal(t, 0, windows[0].Index)
		assert.False(t, windows[0].Active)
		assert.Equal(t, "server", windows[1].Name)
		assert.Equal(t, "@8", windows[1].ID)
		assert.True(t, windows[1].Active)
		output, err := json.Marshal(windows)
		require.NoError(t, err)
		assert.JSONEq(t, `[{"Name":"editor","Path":"/Users/josh/c/sesh","Index":0,"Active":false},{"Name":"server","Path":"/Users/josh/c/sesh","Index":1,"Active":true}]`, string(output), "operational identity must not change public JSON")
	})

	t.Run("target session flag is passed when non-empty", func(t *testing.T) {
		mockShell := &shell.MockShell{}
		tmux := NewTmux(nil, mockShell, "custom-tmux")
		mockShell.EXPECT().ListCmd("custom-tmux", "-u", "list-windows", "-t", "work", "-F", mock.Anything).Return(
			[]string{windowInventoryRow(model.TmuxWindow{ID: "@9", Name: "main", Path: "/home/user", ServerGeneration: generation})},
			nil,
		)
		windows, err := tmux.ListWindows("work")
		assert.Nil(t, err)
		assert.Len(t, windows, 1)
	})

	t.Run("parseTmuxWindowsOutput", func(t *testing.T) {
		raw := []string{windowInventoryRow(model.TmuxWindow{ID: "@7", Name: "editor", Path: "/Users/josh/c/sesh", ServerGeneration: generation}), windowInventoryRow(model.TmuxWindow{ID: "@8", Index: 1, Name: "server", Path: "/Users/josh/c/sesh", Active: true, ServerGeneration: generation})}
		windows, err := parseTmuxWindowsOutput(raw)
		assert.Nil(t, err)
		assert.Len(t, windows, 2)
		assert.Equal(t, "editor", windows[0].Name)
		assert.Equal(t, "@7", windows[0].ID)
		assert.Equal(t, generation, windows[0].ServerGeneration)
		assert.Equal(t, 0, windows[0].Index)
		assert.False(t, windows[0].Active)
		assert.Equal(t, "server", windows[1].Name)
		assert.Equal(t, "@8", windows[1].ID)
		assert.Equal(t, 1, windows[1].Index)
		assert.True(t, windows[1].Active)
	})
}

func TestListAllWindowNames(t *testing.T) {
	t.Run("groups window names by session", func(t *testing.T) {
		mockShell := &shell.MockShell{}
		tmux := &RealTmux{shell: mockShell, bin: "tmux"}
		mockShell.EXPECT().ListCmd("tmux", "list-windows", "-a", "-F", "#{session_name}::#{window_name}").Return(
			[]string{"sesh::editor", "sesh::server", "dotfiles::nvim"},
			nil,
		)
		windowNames, err := tmux.ListAllWindowNames("")
		assert.Nil(t, err)
		assert.Equal(t, map[string][]string{
			"sesh":     {"editor", "server"},
			"dotfiles": {"nvim"},
		}, windowNames)
	})

	t.Run("returns error from shell", func(t *testing.T) {
		mockShell := &shell.MockShell{}
		tmux := &RealTmux{shell: mockShell, bin: "tmux"}
		format := "#{?#{pane_title},#{pane_title},#{window_name}}"
		mockShell.EXPECT().ListCmd("tmux", "list-windows", "-a", "-F", "#{session_name}::"+format).Return(
			nil, assert.AnError,
		)
		windowNames, err := tmux.ListAllWindowNames(format)
		assert.Error(t, err)
		assert.Nil(t, windowNames)
	})

	t.Run("parseAllWindowNamesOutput skips malformed lines", func(t *testing.T) {
		raw := []string{"sesh::editor", "no-separator", "::orphan", "sesh::", "work::make::build"}
		windowNames := parseAllWindowNamesOutput(raw)
		assert.Equal(t, map[string][]string{
			"sesh": {"editor"},
			"work": {"make::build"},
		}, windowNames)
	})
}

func TestListAllWindows(t *testing.T) {
	const generation = "101:1700000000:/tmp/socket"
	mockShell := &shell.MockShell{}
	tmux := &RealTmux{shell: mockShell, bin: "custom-tmux"}
	mockShell.EXPECT().ListCmd("custom-tmux", "-u", "list-windows", "-a", "-F",
		"#{n:session_name}::#{session_name}"+testWindowInventoryFormat).Return(
		[]string{"4::sesh" + windowInventoryRow(model.TmuxWindow{ID: "@7", Index: 1, Name: "editor", Path: "/c/sesh", Active: true, ServerGeneration: generation}), "4::sesh" + windowInventoryRow(model.TmuxWindow{ID: "@8", Index: 2, Name: "server", Path: "/c/sesh", ServerGeneration: generation}), "8::dotfiles" + windowInventoryRow(model.TmuxWindow{ID: "@9", Index: 1, Name: "nvim", Path: "/c/dotfiles", Active: true, ServerGeneration: generation})},
		nil,
	)
	windows, err := tmux.ListAllWindows()
	assert.Nil(t, err)
	assert.Equal(t, map[string][]model.TmuxWindow{
		"sesh": {
			{ID: "@7", ServerGeneration: generation, Name: "editor", Path: "/c/sesh", Index: 1, Active: true},
			{ID: "@8", ServerGeneration: generation, Name: "server", Path: "/c/sesh", Index: 2},
		},
		"dotfiles": {{ID: "@9", ServerGeneration: generation, Name: "nvim", Path: "/c/dotfiles", Index: 1, Active: true}},
	}, windows)
}

func TestWindowInventoryTextCannotShiftIdentity(t *testing.T) {
	ordinary := model.TmuxWindow{ID: "@7", Index: 4, Name: "notes", Path: "/tmp/A", Active: true, ServerGeneration: "101:1700000000:/tmp/socket"}
	for _, field := range []string{"name", "path", "generation socket"} {
		for _, text := range []string{"A::0::@8", "%3A%::\\x3A雪", "#{window_id}::@8::101:1700000000:/other"} {
			t.Run(field+"/"+text, func(t *testing.T) {
				window := ordinary
				switch field {
				case "name":
					window.Name = text
				case "path":
					window.Path = "/tmp/" + text
				case "generation socket":
					window.ServerGeneration = "101:1700000000:/tmp/" + text
				}
				windows, err := parseTmuxWindowsOutput(strings.Split(windowInventoryRow(window)+"\n", "\n"))
				require.NoError(t, err)
				require.Equal(t, []*model.TmuxWindow{&window}, windows)
			})
		}
	}
	// A complete fake record inside a field is data, including when ListCmd
	// splits it into several strings. Its ID must not create an extra window.
	fake := windowInventoryRow(model.TmuxWindow{ID: "@8", Index: 4, Name: "notes", Path: "/other", ServerGeneration: ordinary.ServerGeneration})
	for _, field := range []string{"name", "path"} {
		t.Run(field+"/embedded record", func(t *testing.T) {
			window := ordinary
			if field == "name" {
				window.Name = "notes\n" + fake + "\r\n雪"
			} else {
				window.Path = "/tmp/A\n" + fake + "\r\n雪"
			}
			windows, err := parseTmuxWindowsOutput(strings.Split(windowInventoryRow(window)+"\n"+windowInventoryRow(ordinary)+"\n", "\n"))
			require.NoError(t, err)
			require.Equal(t, []*model.TmuxWindow{&window, &ordinary}, windows)
		})
	}
	for _, session := range []string{"$0", "work::@8::0", "work%3A\\雪\n" + fake} {
		t.Run("session/"+session, func(t *testing.T) {
			row := fmt.Sprintf("%d::%s%s", len(session), session, windowInventoryRow(ordinary))
			windows, err := parseAllWindowsOutput(strings.Split(row+"\n", "\n"))
			require.NoError(t, err)
			require.Equal(t, map[string][]model.TmuxWindow{session: {ordinary}}, windows)
		})
	}
}

func TestWindowInventoryRejectsMalformedRecords(t *testing.T) {
	ordinary := model.TmuxWindow{ID: "@7", Index: 4, Name: "notes", Path: "/tmp/A", ServerGeneration: "101:1700000000:/tmp/socket"}
	valid := windowInventoryRow(ordinary)
	payload := ordinary.Name + ordinary.Path + ordinary.ServerGeneration
	for _, record := range []string{
		"malformed", "4::notes::/tmp/A::0::@7::" + ordinary.ServerGeneration,
		"@7::4::0::5::6", valid[:len(valid)-1], valid + "::@8", valid + "\n\n",
		"@7::::0::5::6::" + fmt.Sprint(len(ordinary.ServerGeneration)) + "::" + payload,
		"@7::04::0::5::6::" + fmt.Sprint(len(ordinary.ServerGeneration)) + "::" + payload,
		"@7::-4::0::5::6::" + fmt.Sprint(len(ordinary.ServerGeneration)) + "::" + payload,
		"@7::4::true::5::6::" + fmt.Sprint(len(ordinary.ServerGeneration)) + "::" + payload,
		"@7::4::0::05::6::" + fmt.Sprint(len(ordinary.ServerGeneration)) + "::" + payload,
		"@7::4::0::-1::6::" + fmt.Sprint(len(ordinary.ServerGeneration)) + "::" + payload,
		"@7::4::0::5::::" + fmt.Sprint(len(ordinary.ServerGeneration)) + "::" + payload,
		"@7::4::0::5::6::18446744073709551616::" + payload,
		"@7::4::0::5::6::" + fmt.Sprint(len(ordinary.ServerGeneration)+1) + "::" + payload,
	} {
		t.Run(record, func(t *testing.T) {
			// Even an earlier valid record must not escape as partial authority.
			windows, err := parseTmuxWindowsOutput([]string{valid, record})
			require.Error(t, err)
			require.Nil(t, windows)
		})
	}
	for _, id := range []string{"", "@", "7", "$7", "@01", "@-1", "@+7", "@name", "@7.0", "@7suffix", "@18446744073709551616", "@7::@8"} {
		t.Run("ID/"+id, func(t *testing.T) {
			window := ordinary
			window.ID = id
			windows, err := parseTmuxWindowsOutput([]string{windowInventoryRow(window)})
			require.Error(t, err)
			require.Nil(t, windows)
		})
	}
	for _, generation := range []string{"", "unknown", "101:0:/tmp/socket", "0:1700000000:/tmp/socket", "0101:1700000000:/tmp/socket", "101:1700000000:", "#{pid}:#{start_time}:#{socket_path}", "0::@7::" + ordinary.ServerGeneration, "101:1700000000:/tmp/\n@8"} {
		t.Run("generation/"+generation, func(t *testing.T) {
			window := ordinary
			window.ServerGeneration = generation
			windows, err := parseTmuxWindowsOutput(strings.Split(windowInventoryRow(window), "\n"))
			require.Error(t, err)
			require.Nil(t, windows)
		})
	}
	for _, sessionPrefix := range []string{"", "0::", "04::work", "-4::work", "18446744073709551616::work", "999::work"} {
		t.Run("session/"+sessionPrefix, func(t *testing.T) {
			windows, err := parseAllWindowsOutput([]string{sessionPrefix + valid})
			require.Error(t, err)
			require.Nil(t, windows)
		})
	}
	windows, err := parseTmuxWindowsOutput(nil)
	require.NoError(t, err)
	require.Empty(t, windows)
}
