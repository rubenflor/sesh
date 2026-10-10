package connector

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/joshmedeski/sesh/v2/dir"
	"github.com/joshmedeski/sesh/v2/execwrap"
	"github.com/joshmedeski/sesh/v2/git"
	"github.com/joshmedeski/sesh/v2/home"
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/namer"
	"github.com/joshmedeski/sesh/v2/oswrap"
	"github.com/joshmedeski/sesh/v2/pathwrap"
	"github.com/joshmedeski/sesh/v2/shell"
	"github.com/joshmedeski/sesh/v2/startup"
	"github.com/joshmedeski/sesh/v2/tmux"
	"github.com/joshmedeski/sesh/v2/zoxide"
)

func TestConnectSelectedDirectoryUsesLiteralPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "literal $SESH_FD_PATH")
	require.NoError(t, os.Mkdir(path, 0700))
	path, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	t.Setenv("SESH_FD_PATH", "WRONG_DIRECTORY")
	config := model.Config{DirLength: 1}
	osw, pw := oswrap.NewOs(), pathwrap.NewPath()
	h := home.NewHome(osw)
	sh := shell.NewShell(execwrap.NewExec(), h)
	g := git.NewGit(sh)
	mt := tmux.NewMockTmux(t)
	mt.EXPECT().IsAttached().Return(false).Maybe()
	// A local display name may coincide with the selected full directory path.
	inventory := []*model.TmuxSession{{ID: "$1", ServerGeneration: existingSessionGeneration, Name: path, Path: "/wrong"}}
	mt.EXPECT().ListSessions().RunAndReturn(func() ([]*model.TmuxSession, error) { return inventory, nil })
	mt.EXPECT().NewSession("literal_$SESH_FD_PATH", path).Run(func(string, string) {
		inventory = append(inventory, &model.TmuxSession{ID: "$8", ServerGeneration: existingSessionGeneration, Name: "literal_$SESH_FD_PATH", Path: path})
	}).Return("", nil).Once()
	mt.EXPECT().SwitchOrAttach("$8", model.ConnectOpts{}).Return("connected $8", nil).Once()
	zo := zoxide.NewMockZoxide(t)
	zo.EXPECT().Add(path).Return(nil).Once()
	st := startup.NewMockStartup(t)
	st.EXPECT().Exec(model.SeshSession{Src: "dir", Name: "literal_$SESH_FD_PATH", Path: path}, "$8").Return("", nil).Once()
	li := lister.NewLister(config, h, mt, nil, nil, nil, nil, nil)
	c := NewConnector(config, dir.NewDir(osw, g, pw), h, li, namer.NewNamer(pw, g, h, config), st, mt, zo, nil, nil)
	message, err := c.ConnectSession(model.SeshSession{Src: "dir", Name: path, Path: path}, model.ConnectOpts{})
	require.NoError(t, err)
	require.Equal(t, "connected literal_$SESH_FD_PATH", message)
}
