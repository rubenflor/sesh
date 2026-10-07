package previewer

import (
	"github.com/joshmedeski/sesh/v2/lister"
	"github.com/joshmedeski/sesh/v2/model"
	"github.com/joshmedeski/sesh/v2/tmux"
)

type TmuxPreviewStrategy struct {
	lister lister.Lister
	tmux   tmux.Tmux
}

func NewTmuxStrategy(lister lister.Lister, tmux tmux.Tmux) *TmuxPreviewStrategy {
	return &TmuxPreviewStrategy{lister: lister, tmux: tmux}
}

func (s *TmuxPreviewStrategy) Execute(name string) (string, error) {
	session, sessionExists := s.lister.FindTmuxSession(name)

	if sessionExists {
		return PreviewTmuxSession(s.tmux, session)
	}

	return "", nil
}

// PreviewTmuxSession retains the selected row across captures, including watch
// frames. A renamed session remains the target; a replacement never does.
func PreviewTmuxSession(t tmux.Tmux, session model.SeshSession) (string, error) {
	if _, err := tmux.VerifyLocalSession(t, session.TmuxSessionID, session.TmuxServerGeneration); err != nil {
		return "", err
	}
	return t.CapturePane(session.TmuxSessionID, session.TmuxServerGeneration)
}
