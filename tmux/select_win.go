package tmux

func (t *RealTmux) SelectWindow(targetWindow string, serverGeneration ...string) (string, error) {
	return t.sessionCommand(t.bin, targetWindow, serverGeneration, "select-window", "-t", targetWindow)
}
