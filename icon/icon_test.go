package icon

import (
	"testing"

	"github.com/joshmedeski/sesh/v2/ansi"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/stretchr/testify/require"
)

func TestRemoveIconPreservesLightningPrefixedNames(t *testing.T) {
	i := NewIcon(model.Config{})

	require.Equal(t, "⚡ foo", i.RemoveIcon("⚡ foo"))
	require.Equal(t, "foo", i.RemoveIcon(tmuxIcon+" foo"))
	require.Equal(t, "foo", i.RemoveIcon(ansiString(ansi.Blue, tmuxIcon)+" foo"))
}
