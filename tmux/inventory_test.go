package tmux

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/stretchr/testify/require"
)

func rawInventoryResult(output string) execwrap.CommandResult {
	return execwrap.CommandResult{Stdout: []byte(output)}
}

func quoteInventoryField(value string) string {
	var quoted strings.Builder
	quoted.WriteByte('"')
	for i := 0; i < len(value); i++ {
		if strings.IndexByte("|&;<>()$`\\\"'*?[# =%", value[i]) >= 0 {
			quoted.WriteByte('\\')
		}
		quoted.WriteByte(value[i])
	}
	quoted.WriteByte('"')
	return quoted.String()
}

func inventoryEncodedFields(schema inventorySchema, fields []string) []string {
	encoded := append([]string(nil), fields...)
	for i, format := range schema.fields {
		if i < len(fields) && strings.HasPrefix(format, `"`) {
			encoded[i] = quoteInventoryField(fields[i])
		}
	}
	return encoded
}

func inventoryRow(schema inventorySchema, fields []string) string {
	return schema.tag + "::" + strings.Join(inventoryEncodedFields(schema, fields), "::")
}

func TestInventoryCodecRoundTripsBytes(t *testing.T) {
	schema := inventorySchema{tag: "T1", fields: []string{`"#{q:value}"`}}
	var everyByte strings.Builder
	for b := 1; b <= 255; b++ {
		everyByte.WriteByte(byte(b))
	}
	for _, value := range []string{"", "::", "\n\t\r", `"\\\n`, "café雪", everyByte.String(), "P1::0::\"fake\"::0::\"title\"::%0::$0::@0\nS1::fake"} {
		records, err := schema.decode(inventoryRow(schema, []string{value}))
		require.NoError(t, err)
		require.Equal(t, [][]string{{value}}, records)
	}
}

func TestInventoryCodecRandomRecords(t *testing.T) {
	schema := inventorySchema{tag: "T1", fields: make([]string, 8)}
	for i := range schema.fields {
		schema.fields[i] = `"#{q:value}"`
	}
	rng := rand.New(rand.NewSource(42))
	var output strings.Builder
	want := make([][]string, 5000)
	for i := range want {
		want[i] = make([]string, len(schema.fields))
		for j := range want[i] {
			value := make([]byte, rng.Intn(128))
			for k := range value {
				value[k] = byte(1 + rng.Intn(255))
			}
			want[i][j] = string(value)
		}
		output.WriteString(inventoryRow(schema, want[i]))
		output.WriteByte('\n')
	}
	got, err := schema.decode(output.String())
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestInventoryCodecRejectsMalformedBatches(t *testing.T) {
	schema := inventorySchema{tag: "T1", fields: []string{`"#{q:value}"`, "#{pane_id}"}}
	valid := inventoryRow(schema, []string{"before\n::after\t", "%1"})
	for i, bad := range []string{
		`T2::"unknown"::%1`, `P1::"wrong schema"::%1`, "T1", `T1::unquoted::%1`,
		`T1::"unterminated`, "T1::\"dangling\\", `T1::"bad\n"::%1`, `T1::"bad\q"::%1`,
		`T1::"bad\012"::%1`, "T1::\"NUL\x00\"::%1", `T1::"extra"::%1::"field"`,
		`T1::"missing"`, valid + "::garbage", valid + "\n\n", valid + "\r\n",
		`T1::"value"::"%1"`, `T1::"value"::%1\`, "T1::\"value\"::%1\x00",
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			records, err := schema.decode(valid + "\n" + bad)
			require.Error(t, err)
			require.Nil(t, records)
		})
	}
}

func TestInventoryCodecPreservesEveryArbitraryField(t *testing.T) {
	for _, schema := range []inventorySchema{sessionInventorySchema, paneInventorySchema} {
		fields := sessionInventoryFields()
		if schema.tag == paneInventorySchema.tag {
			fields = paneInventoryFields()
		}
		for i, format := range schema.fields {
			if !strings.HasPrefix(format, `"`) {
				continue
			}
			for _, text := range []string{"", " ", "::", `"'`, `\`, "\n", "\t", "café雪", "$0 @0 %0", schema.format(), "before\n" + inventoryRow(schema, fields) + "\nafter"} {
				values := append([]string(nil), fields...)
				values[i] = text
				records, err := schema.decode(inventoryRow(schema, values) + "\n" + inventoryRow(schema, fields))
				require.NoError(t, err, format)
				require.Equal(t, [][]string{values, fields}, records, format)
			}
		}
	}
}

func TestInventoryCodecLimits(t *testing.T) {
	schema := inventorySchema{tag: "T1", fields: []string{`"#{q:value}"`}}
	for _, output := range []string{
		strings.Repeat("x", maxInventoryBytes+1),
		strings.Repeat("T1::\"\"\n", maxInventoryRecords+1),
	} {
		records, err := schema.decode(output)
		require.Error(t, err)
		require.Nil(t, records)
	}
}

func TestInventoryFormatsEvaluateArbitraryFieldsOnce(t *testing.T) {
	for _, schema := range []inventorySchema{sessionInventorySchema, paneInventorySchema} {
		format := schema.format()
		require.NotContains(t, format, "#{n:")
		for _, field := range schema.fields {
			require.Equal(t, 1, strings.Count(format, field), field)
			if strings.HasPrefix(field, `"`) {
				require.Contains(t, field, "#{q:")
				require.True(t, strings.HasSuffix(field, `}"`), field)
			}
		}
		require.Equal(t, 1, strings.Count(format, "#{pid}"))
		require.Equal(t, 1, strings.Count(format, "#{start_time}"))
		require.Equal(t, 1, strings.Count(format, "#{q:socket_path}"))
	}
}

func FuzzInventoryDecoder(f *testing.F) {
	for _, schema := range []inventorySchema{sessionInventorySchema, paneInventorySchema} {
		fields := sessionInventoryFields()
		if schema.tag == paneInventorySchema.tag {
			fields = paneInventoryFields()
		}
		valid := inventoryRow(schema, fields)
		f.Add(valid)
		f.Add(valid + "\n" + valid)
		f.Add(valid + "\nmalformed")
	}
	f.Add("P1::0::\"before\n::after\\\"\"::0")
	f.Fuzz(func(t *testing.T, output string) {
		for _, schema := range []inventorySchema{sessionInventorySchema, paneInventorySchema} {
			records, err := schema.decode(output)
			if err != nil {
				if records != nil {
					t.Fatal("partial inventory on failure")
				}
				continue
			}
			var encoded strings.Builder
			for _, fields := range records {
				encoded.WriteString(inventoryRow(schema, fields))
				encoded.WriteByte('\n')
			}
			canonical := encoded.String()
			roundTrip, err := schema.decode(canonical)
			if len(canonical) > maxInventoryBytes {
				require.Error(t, err)
				require.Nil(t, roundTrip)
				continue
			}
			require.NoError(t, err)
			require.Equal(t, records, roundTrip)
		}
		if sessions, err := parseTmuxSessionsOutput(output); err != nil && sessions != nil {
			t.Fatal("partial session identities on failure")
		}
		if panes, err := parseTmuxPanesOutput(output); err != nil && panes != nil {
			t.Fatal("partial pane identities on failure")
		}
	})
}
