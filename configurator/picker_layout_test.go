package configurator

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPickerLayoutConfiguration(t *testing.T) {
	config, err := configFromTOML(t, "strict_mode = true\n[tui]\ninput_position = 'bottom'\nkeymap = 'fzf'\n")
	require.NoError(t, err)
	require.Equal(t, "bottom", config.TUI.InputPosition)
	require.Equal(t, "fzf", config.TUI.Keymap)
}

func TestPickerLayoutDefaultsAndValidation(t *testing.T) {
	config, err := configFromTOML(t, "")
	require.NoError(t, err)
	require.Equal(t, "top", config.TUI.InputPosition)
	require.Equal(t, "default", config.TUI.Keymap)
	for _, raw := range []string{
		"[tui]\ninput_position = 'middle'\n",
		"[tui]\nkeymap = 'unknown'\n",
	} {
		_, err := configFromTOML(t, raw)
		require.Error(t, err)
	}
}
