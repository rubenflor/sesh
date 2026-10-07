package tmux

func (t *RealTmux) SelectWindow(targetWindow string, serverGeneration ...string) (string, error) {
	return t.sessionCommand("tmux", targetWindow, serverGeneration, "select-window", "-t", targetWindow)
}
