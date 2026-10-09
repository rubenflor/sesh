package tmux

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const testWindowInventoryFormat = `W1::#{session_id}::"#{q:session_name}"::#{window_id}::#{window_index}::#{window_active}::"#{q:window_name}"::"#{q:pane_current_path}"::"#{pid}:#{start_time}:#{q:socket_path}"`

func windowInventoryFields() []string {
	return []string{"$1", "sesh", "@7", "4", "1", "notes", "/tmp/A", "101:1700000000:/tmp/socket"}
}

func windowInventoryRow(window model.TmuxWindow) string {
	active := 0
	if window.Active {
		active = 1
	}
	return inventoryRow(windowInventorySchema, []string{"$1", "sesh", window.ID, strconv.Itoa(window.Index), strconv.Itoa(active), window.Name, window.Path, window.ServerGeneration})
}

func TestListWindows(t *testing.T) {
	const generation = "101:1700000000:/tmp/socket::suffix"
	t.Run("returns parsed windows", func(t *testing.T) {
		mockShell := &shell.MockShell{}
		tmux := NewTmux(nil, mockShell, "")
		mockShell.EXPECT().CmdCaptureRaw(mock.Anything, "tmux", maxInventoryBytes, maxInventoryStderr, "-u", "list-windows", "-F", testWindowInventoryFormat).Return(
			rawInventoryResult(strings.Join([]string{windowInventoryRow(model.TmuxWindow{ID: "@7", Index: 0, Name: "editor", Path: "/Users/josh/c/sesh", ServerGeneration: generation}), windowInventoryRow(model.TmuxWindow{ID: "@8", Index: 1, Name: "server", Path: "/Users/josh/c/sesh", Active: true, ServerGeneration: generation}), ""}, "\n")),
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
		mockShell.EXPECT().CmdCaptureRaw(mock.Anything, "custom-tmux", maxInventoryBytes, maxInventoryStderr, "-u", "list-windows", "-t", "work", "-F", testWindowInventoryFormat).Return(
			rawInventoryResult(windowInventoryRow(model.TmuxWindow{ID: "@9", Name: "main", Path: "/home/user", ServerGeneration: generation})),
			nil,
		)
		windows, err := tmux.ListWindows("work")
		assert.Nil(t, err)
		assert.Len(t, windows, 1)
	})

	t.Run("parseTmuxWindowsOutput", func(t *testing.T) {
		raw := strings.Join([]string{windowInventoryRow(model.TmuxWindow{ID: "@7", Name: "editor", Path: "/Users/josh/c/sesh", ServerGeneration: generation}), windowInventoryRow(model.TmuxWindow{ID: "@8", Index: 1, Name: "server", Path: "/Users/josh/c/sesh", Active: true, ServerGeneration: generation})}, "\n")
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
	mockShell.EXPECT().CmdCaptureRaw(mock.Anything, "custom-tmux", maxInventoryBytes, maxInventoryStderr, "-u", "list-windows", "-a", "-F", testWindowInventoryFormat).Return(
		rawInventoryResult(strings.Join([]string{windowInventoryRow(model.TmuxWindow{ID: "@7", Index: 1, Name: "editor", Path: "/c/sesh", Active: true, ServerGeneration: generation}), windowInventoryRow(model.TmuxWindow{ID: "@8", Index: 2, Name: "server", Path: "/c/sesh", ServerGeneration: generation}), inventoryRow(windowInventorySchema, []string{"$2", "dotfiles", "@9", "1", "1", "nvim", "/c/dotfiles", generation})}, "\n")),
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
		for _, text := range []string{"", " ", ":", "::", `"'`, `\`, "\t", "café雪", "$0 @0 %0", "A::0::@8", "%3A%::\\x3A雪", "#{window_id}::@8::101:1700000000:/other"} {
			t.Run(field+"/"+text, func(t *testing.T) {
				window := ordinary
				switch field {
				case "name":
					window.Name = text
				case "path":
					window.Path = text
				case "generation socket":
					window.ServerGeneration = "101:1700000000:/tmp/" + text
				}
				windows, err := parseTmuxWindowsOutput(windowInventoryRow(window) + "\n")
				require.NoError(t, err)
				require.Equal(t, []*model.TmuxWindow{&window}, windows)
				all, err := parseAllWindowsOutput(windowInventoryRow(window) + "\n")
				require.NoError(t, err)
				require.Equal(t, map[string][]model.TmuxWindow{"sesh": {window}}, all)
			})
		}
	}
	// A complete fake record inside a quoted field must not create another window.
	fake := windowInventoryRow(model.TmuxWindow{ID: "@8", Index: 4, Name: "notes", Path: "/other", ServerGeneration: ordinary.ServerGeneration})
	for _, field := range []string{"name", "path"} {
		t.Run(field+"/embedded record", func(t *testing.T) {
			window := ordinary
			if field == "name" {
				window.Name = "notes\n" + fake + "\r\n雪"
			} else {
				window.Path = "/tmp/A\n" + fake + "\r\n雪"
			}
			output := windowInventoryRow(window) + "\n" + windowInventoryRow(ordinary) + "\n"
			windows, err := parseTmuxWindowsOutput(output)
			require.NoError(t, err)
			require.Equal(t, []*model.TmuxWindow{&window, &ordinary}, windows)
			all, err := parseAllWindowsOutput(output)
			require.NoError(t, err)
			require.Equal(t, map[string][]model.TmuxWindow{"sesh": {window, ordinary}}, all)
		})
	}
	for _, session := range []string{" ", ":", "::", `"'`, `\`, "\n", "\t", "café雪", "$0 @0 %0", "work::@8::0", "work%3A\\雪\n" + fake} {
		t.Run("session/"+session, func(t *testing.T) {
			fields := windowInventoryFields()
			fields[1] = session
			row := inventoryRow(windowInventorySchema, fields)
			windows, err := parseAllWindowsOutput(row + "\n")
			require.NoError(t, err)
			require.Equal(t, map[string][]model.TmuxWindow{session: {ordinary}}, windows)
			scoped, err := parseTmuxWindowsOutput(row)
			require.NoError(t, err)
			require.Equal(t, []*model.TmuxWindow{&ordinary}, scoped)
		})
	}
}

func TestWindowInventoryRejectsMalformedRecords(t *testing.T) {
	fields := windowInventoryFields()
	valid := inventoryRow(windowInventorySchema, fields)
	assertMalformed := func(t *testing.T, bad string) {
		t.Helper()
		for _, output := range []string{bad, valid + "\n" + bad} {
			windows, err := parseTmuxWindowsOutput(output)
			require.Error(t, err)
			require.Nil(t, windows, "no earlier valid window may escape")
			all, err := parseAllWindowsOutput(output)
			require.Error(t, err)
			require.Nil(t, all, "no earlier valid session/window association may escape")
		}
	}
	for i, record := range []string{
		"malformed", strings.Replace(valid, "W1", "W2", 1), strings.Replace(valid, "W1", "S1", 1),
		inventoryRow(windowInventorySchema, fields[:7]), inventoryRow(windowInventorySchema, append(fields, "extra")),
		valid[:len(valid)-1], valid + "::@8", valid + "\n\n", valid + "\r\n", "\n" + valid,
		strings.Replace(valid, `"notes"`, `notes`, 1), strings.Replace(valid, `"notes"`, `"bad\q"`, 1),
		strings.Replace(valid, `"notes"`, "\"NUL\x00\"", 1), strings.Replace(valid, "$1", `"$1"`, 1),
		strings.Repeat("x", maxInventoryBytes+1), strings.Repeat(valid+"\n", maxInventoryRecords+1),
	} {
		t.Run("wire/"+fmt.Sprint(i), func(t *testing.T) { assertMalformed(t, record) })
	}
	for field, invalid := range map[int][]string{
		0: {"", "$", "1", "@1", "$01", "$-1", "$+1", "$name", "$1suffix", "$18446744073709551616"},
		1: {""},
		2: {"", "@", "7", "$7", "@01", "@-1", "@+7", "@name", "@7.0", "@7suffix", "@18446744073709551616", "@7::@8"},
		3: {"", "04", "-4", "+4", "4.0", "18446744073709551616"},
		4: {"", "true", "2", "01"},
		7: {"", "unknown", "101:0:/tmp/socket", "0:1700000000:/tmp/socket", "0101:1700000000:/tmp/socket", "101:1700000000:", "#{pid}:#{start_time}:#{socket_path}", "0::@7::" + fields[7], "101:1700000000:/tmp/\n@8", "101:1700000000:/tmp/\r@8"},
	} {
		for _, value := range invalid {
			t.Run(fmt.Sprintf("field %d/%s", field, value), func(t *testing.T) {
				values := append([]string(nil), fields...)
				values[field] = value
				assertMalformed(t, inventoryRow(windowInventorySchema, values))
			})
		}
	}
	windows, err := parseTmuxWindowsOutput("")
	require.NoError(t, err)
	require.Empty(t, windows)
	all, err := parseAllWindowsOutput("")
	require.NoError(t, err)
	require.Empty(t, all)
}

func TestWindowInventoryRejectsPairedPartitions(t *testing.T) {
	ordinary := model.TmuxWindow{ID: "@7", Index: 4, Active: true, Name: "notes", Path: "/tmp/A", ServerGeneration: "101:1700000000:/tmp/socket"}
	gen := ordinary.ServerGeneration
	fake := fmt.Sprintf("@8::5::0::5::6::%d::other/tmp/B", len(gen))
	for _, test := range []struct {
		name, path     string
		nameSize, size int
	}{
		// Compensating stale lengths accept name "note" and path "s/tmp/A".
		{"wrong field partition", ordinary.Path, 4, 7},
		// A short stale cwd length exposes generation + a fake second record.
		{"extra record", ordinary.Path + gen + "\n" + fake, 5, 6},
	} {
		t.Run(test.name, func(t *testing.T) {
			legacy := fmt.Sprintf("@7::4::1::%d::%d::%d::%s%s%s", test.nameSize, test.size, len(gen), ordinary.Name, test.path, gen)
			windows, err := parseTmuxWindowsOutput(legacy)
			require.Error(t, err, "paired-length records are not a supported schema")
			require.Nil(t, windows)
			all, err := parseAllWindowsOutput("4::sesh" + legacy)
			require.Error(t, err)
			require.Nil(t, all)
			window := ordinary
			window.Path = test.path
			quoted := windowInventoryRow(window)
			windows, err = parseTmuxWindowsOutput(quoted)
			require.NoError(t, err)
			require.Equal(t, []*model.TmuxWindow{&window}, windows, "one native record stays one complete window")
			all, err = parseAllWindowsOutput(quoted)
			require.NoError(t, err)
			require.Equal(t, map[string][]model.TmuxWindow{"sesh": {window}}, all)
		})
	}
}

func TestListWindowsRejectsEmptyVerifiedCapture(t *testing.T) {
	s := shell.NewMockShell(t)
	s.EXPECT().CmdCaptureRaw(mock.Anything, "custom-tmux", maxInventoryBytes, maxInventoryStderr, "-u", "if-shell", "-F", "-t", "$1:", mock.Anything,
		"'list-windows' '-t' '$1' '-F' '"+testWindowInventoryFormat+"'", "if-shell").Return(rawInventoryResult(""), nil).Once()
	windows, err := NewTmux(nil, s, "custom-tmux").ListWindows("$1", "101:1700000000:/tmp/socket")
	require.Error(t, err)
	require.Nil(t, windows)
}
