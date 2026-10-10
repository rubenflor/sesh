package previewer

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/joshmedeski/sesh/v2/dir"
	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/home"
	"github.com/joshmedeski/sesh/v2/ls"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/oswrap"
	"github.com/joshmedeski/sesh/v2/pathwrap"
	"github.com/joshmedeski/sesh/v2/shell"
)

func TestLiteralDirectoryPreviewBoundaries(t *testing.T) {
	osw := oswrap.NewOs()
	h := home.NewHome(osw)
	sh := shell.NewShell(execwrap.NewExec(), h)
	config := model.Config{}
	// No name-based strategies can run: their dependencies are deliberately absent.
	p := NewPreviewer(nil, nil, nil, dir.NewDir(osw, nil, pathwrap.NewPath()), h,
		ls.NewLs(config, sh), config, sh).(LiteralDirectoryPreviewer)
	for _, path := range []string{"", "relative directory", "$SESH_T56_PREVIEW_COMPONENT"} {
		output, err := p.PreviewDirectory(path)
		require.Error(t, err)
		require.Empty(t, output)
	}
	root := t.TempDir()
	output, err := p.PreviewDirectory(root)
	require.NoError(t, err)
	require.Empty(t, output, "successful empty directory previews are exclusive")
	output, err = p.PreviewDirectory(filepath.Join(root, "missing"))
	require.NoError(t, err)
	require.Empty(t, output, "a disappeared directory must not fall back to name resolution")
}
