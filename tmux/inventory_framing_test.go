package tmux

import (
	"fmt"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestSessionInventoryTextCannotShiftIdentity(t *testing.T) {
	ordinary := sessionInventoryFields()
	next := append([]string(nil), ordinary...)
	next[13], next[17] = "$2", "next session"
	fake := append([]string(nil), ordinary...)
	fake[13], fake[17] = "$0", "fake"
	texts := []string{"", " ", "::", ":", `"'`, "\n", "\t", "%", `\`, "café雪", "$0", "@0", "%0", ordinary[21], "before\n" + sessionInventoryRow(fake) + "\nafter"}
	for _, field := range []struct {
		index int
		name  string
		get   func(*model.TmuxSession) string
	}{
		{1, "alerts", func(s *model.TmuxSession) string { return strings.Join(s.Alerts, ",") }},
		{3, "attached clients", func(s *model.TmuxSession) string { return strings.Join(s.AttachedList, ",") }},
		{6, "group", func(s *model.TmuxSession) string { return s.Group }},
		{8, "group attached clients", func(s *model.TmuxSession) string { return strings.Join(s.GroupAttachedList, ",") }},
		{9, "group sessions", func(s *model.TmuxSession) string { return strings.Join(s.GroupList, ",") }},
		{17, "name", func(s *model.TmuxSession) string { return s.Name }},
		{18, "path", func(s *model.TmuxSession) string { return s.Path }},
		{21, "generation socket", func(s *model.TmuxSession) string { return s.ServerGeneration }},
	} {
		for _, value := range texts {
			t.Run(field.name+"/"+value, func(t *testing.T) {
				fields := append([]string(nil), ordinary...)
				if field.index == 21 {
					value = "101:1700000000:/tmp/" + value
				}
				fields[field.index] = value
				sessions, err := parseTmuxSessionsOutput(sessionInventoryRow(fields) + "\n" + sessionInventoryRow(next) + "\n")
				if field.index == 21 && strings.Contains(value, "\n") {
					require.Error(t, err)
					require.Nil(t, sessions)
					return
				}
				require.NoError(t, err)
				require.Len(t, sessions, 2, "embedded inventory is only field data")
				require.Equal(t, value, field.get(sessions[0]))
				require.Equal(t, "$1", sessions[0].ID)
				require.Equal(t, "$2", sessions[1].ID)
				require.Equal(t, fields[21], sessions[0].ServerGeneration)
				require.Equal(t, ordinary[21], sessions[1].ServerGeneration)
				require.Equal(t, next[17], sessions[1].Name)
			})
		}
	}
}

func TestPaneInventoryTextCannotShiftIdentity(t *testing.T) {
	ordinary := paneInventoryFields()
	next := append([]string(nil), ordinary...)
	next[6], next[7], next[8], next[1] = "%8", "$2", "@4", "next window"
	fake := append([]string(nil), ordinary...)
	fake[6], fake[7], fake[8] = "%0", "$0", "@0"
	texts := []string{"", " ", "::", ":", `"'`, "\n", "\t", "%", `\`, "café雪", "$0", "@0", "%0", ordinary[9], "before\n" + paneInventoryRow(fake) + "\nafter"}
	for _, field := range []struct {
		index int
		name  string
		get   func(*model.TmuxPane) string
	}{
		{1, "window name", func(p *model.TmuxPane) string { return p.WindowName }},
		{3, "title", func(p *model.TmuxPane) string { return p.PaneTitle }},
		{4, "command", func(p *model.TmuxPane) string { return p.PaneCommand }},
		{5, "path", func(p *model.TmuxPane) string { return p.PanePath }},
		{9, "generation socket", func(p *model.TmuxPane) string { return p.ServerGeneration }},
	} {
		for _, value := range texts {
			t.Run(field.name+"/"+value, func(t *testing.T) {
				fields := append([]string(nil), ordinary...)
				if field.index == 9 {
					value = "101:1700000000:/tmp/" + value
				}
				fields[field.index] = value
				panes, err := parseTmuxPanesOutput(paneInventoryRow(fields) + "\n" + paneInventoryRow(next) + "\n")
				if field.index == 9 && strings.Contains(value, "\n") {
					require.Error(t, err)
					require.Nil(t, panes)
					return
				}
				require.NoError(t, err)
				require.Len(t, panes, 2)
				require.Equal(t, value, field.get(panes[0]))
				require.Equal(t, "$1", panes[0].SessionID)
				require.Equal(t, "@3", panes[0].WindowID)
				require.Equal(t, "%7", panes[0].PaneID)
				require.Equal(t, "$2", panes[1].SessionID)
				require.Equal(t, "@4", panes[1].WindowID)
				require.Equal(t, "%8", panes[1].PaneID)
				require.Equal(t, fields[9], panes[0].ServerGeneration)
				require.Equal(t, ordinary[9], panes[1].ServerGeneration)
				require.Equal(t, next[1], panes[1].WindowName)
			})
		}
	}
}

func TestSessionPaneInventoriesRejectMalformedRecords(t *testing.T) {
	for _, inventory := range []struct {
		name       string
		fields     []string
		schema     inventorySchema
		idFields   map[int]string
		generation int
		row        func([]string) string
		parse      func(string) (bool, error)
	}{
		{"session", sessionInventoryFields(), sessionInventorySchema, map[int]string{13: "$"}, 21, sessionInventoryRow, func(raw string) (bool, error) {
			sessions, err := parseTmuxSessionsOutput(raw)
			return sessions == nil, err
		}},
		{"pane", paneInventoryFields(), paneInventorySchema, map[int]string{6: "%", 7: "$", 8: "@"}, 9, paneInventoryRow, func(raw string) (bool, error) {
			panes, err := parseTmuxPanesOutput(raw)
			return panes == nil, err
		}},
	} {
		t.Run(inventory.name, func(t *testing.T) {
			valid := inventory.row(inventory.fields)
			assertRejected := func(t *testing.T, record string) {
				t.Helper()
				none, err := inventory.parse(valid + "\n" + record)
				require.Error(t, err)
				require.True(t, none, "no earlier row can escape as partial authority")
			}
			for i, record := range []string{"malformed", "0::", valid[:len(valid)-1], valid + "unexpected", valid + "\n\n", valid + "::extra", strings.Replace(valid, inventory.schema.tag+"::", "V99::", 1)} {
				t.Run(fmt.Sprintf("record/%d", i), func(t *testing.T) { assertRejected(t, record) })
			}
			for i, format := range inventory.schema.fields {
				if !strings.HasPrefix(format, `"`) {
					continue
				}
				for j, malformed := range []string{"", "unquoted", `"unterminated`, `"bad\q"`, `"bad\n"`, `"bad\012"`, "\"dangling\\", `"closed"garbage`, "\"NUL\x00\""} {
					t.Run(fmt.Sprintf("quote/%d/%d", i, j), func(t *testing.T) {
						encoded := inventoryEncodedFields(inventory.schema, inventory.fields)
						encoded[i] = malformed
						assertRejected(t, inventory.schema.tag+"::"+strings.Join(encoded, "::"))
					})
				}
			}
			for i, prefix := range inventory.idFields {
				for _, id := range []string{"", "display", prefix, prefix + "01", prefix + "+1", prefix + "-1", prefix + "1suffix", prefix + "1::" + prefix + "0", prefix + "18446744073709551616"} {
					t.Run(fmt.Sprintf("ID/%d/%s", i, id), func(t *testing.T) {
						fields := append([]string(nil), inventory.fields...)
						fields[i] = id
						assertRejected(t, inventory.row(fields))
					})
				}
			}
			for _, generation := range []string{"", "unknown", "0:1700000000:/tmp/socket", "101:0:/tmp/socket", "0101:1700000000:/tmp/socket", "101:1700000000:", serverGenerationFormat, "101:1700000000:/tmp/socket\n" + valid} {
				t.Run("generation/"+generation, func(t *testing.T) {
					fields := append([]string(nil), inventory.fields...)
					fields[inventory.generation] = generation
					assertRejected(t, inventory.row(fields))
				})
			}
			for i := range inventory.fields {
				t.Run(fmt.Sprintf("missing field/%d", i), func(t *testing.T) {
					header := inventoryEncodedFields(inventory.schema, inventory.fields)
					header = append(header[:i], header[i+1:]...)
					assertRejected(t, inventory.schema.tag+"::"+strings.Join(header, "::"))
				})
			}
		})
	}
}

func TestSessionInventoryRejectsMalformedNumbers(t *testing.T) {
	for _, i := range []int{0, 2, 4, 5, 7, 10, 11, 12, 14, 15, 16, 20} {
		for _, value := range []string{"text", "-1", "+1", "01", "18446744073709551616", "\n$0"} {
			t.Run(fmt.Sprintf("%d/%s", i, value), func(t *testing.T) {
				fields := sessionInventoryFields()
				fields[i] = value
				sessions, err := parseTmuxSessionsOutput(sessionInventoryRow(fields))
				require.Error(t, err)
				require.Nil(t, sessions)
			})
		}
	}
}

func TestPaneInventoryRejectsMalformedIndexes(t *testing.T) {
	for _, i := range []int{0, 2} {
		for _, value := range []string{"", "text", "-1", "+1", "01", "18446744073709551616"} {
			fields := paneInventoryFields()
			fields[i] = value
			panes, err := parseTmuxPanesOutput(paneInventoryRow(fields))
			require.Error(t, err)
			require.Nil(t, panes)
		}
	}
}

func TestMalformedInventoryCannotAuthorizeOwnershipLookup(t *testing.T) {
	for _, kind := range []string{"sessions", "panes"} {
		for _, malformed := range []string{"record", "ID", "generation", "number"} {
			t.Run(kind+"/"+malformed, func(t *testing.T) {
				s := shell.NewMockShell(t)
				if kind == "sessions" {
					bad := sessionInventoryFields()
					bad[13] = "$2"
					switch malformed {
					case "ID":
						bad[13] = "$01"
					case "generation":
						bad[21] = "unknown"
					case "number":
						bad[2] = "-1"
					}
					row := sessionInventoryRow(bad)
					if malformed == "record" {
						row = "malformed"
					}
					s.EXPECT().CmdCaptureRaw(mock.Anything, "custom-tmux", maxInventoryBytes, maxInventoryStderr, "-u", "list-sessions", "-F", listsessionsformat()).Return(rawInventoryResult(sessionInventoryRow(sessionInventoryFields())+"\n"+row), nil).Once()
					sessions, err := NewTmux(nil, s, "custom-tmux").ListSessions()
					require.Error(t, err)
					require.Nil(t, sessions)
				} else {
					bad := paneInventoryFields()
					bad[6] = "%8"
					switch malformed {
					case "ID":
						bad[7] = "$01"
					case "generation":
						bad[9] = "unknown"
					case "number":
						bad[0] = "-1"
					}
					row := paneInventoryRow(bad)
					if malformed == "record" {
						row = "malformed"
					}
					s.EXPECT().CmdCaptureRaw(mock.Anything, "custom-tmux", maxInventoryBytes, maxInventoryStderr, "-u", "list-panes", "-s", "-F", listpanesformat()).Return(rawInventoryResult(paneInventoryRow(paneInventoryFields())+"\n"+row), nil).Once()
					panes, err := NewTmux(nil, s, "custom-tmux").ListTmuxPanes()
					require.Error(t, err)
					require.Nil(t, panes)
				}
			})
		}
	}
}
