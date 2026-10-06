package previewer

import (
	"github.com/joshmedeski/sesh/v2/lister"
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
		sessionID, err := tmux.LocalSessionID(s.tmux, session.Name)
		if err != nil {
			return "", err
		}
		output, err := s.tmux.CapturePane(sessionID)
		if err != nil {
			return "", err
		}

		return output, nil
	}

	return "", nil
}
