package seshcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/joshmedeski/sesh/v2/previewer"
)

const previewClearScreen = "\033[2J\033[H"

type previewFunc func(string) (string, error)

func writePreview(w io.Writer, preview previewFunc, name string) error {
	output, err := preview(name)
	if err != nil {
		return err
	}

	_, err = fmt.Fprint(w, output)
	return err
}

func watchPreview(ctx context.Context, w io.Writer, preview previewFunc, name string, ticks <-chan time.Time) error {
	last := ""
	for {
		output, err := preview(name)
		if err != nil {
			return err
		}

		// Redraw only when there is new content to show. Rewriting an
		// identical frame makes fzf clear and repaint a preview that did
		// not change, and an empty capture (the session went away
		// mid-watch with nothing else matching the name) would blank the
		// preview instead of keeping the last frame on screen.
		if output != "" && output != last {
			frame := output
			if last != "" {
				frame = previewClearScreen + frame
			}
			if _, err := fmt.Fprint(w, frame); err != nil {
				return err
			}
			last = output
		}

		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-ticks:
			if !ok {
				return nil
			}
		}
	}
}

func NewPreviewCommand(base *BaseDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "preview",
		Aliases: []string{"p"},
		Short:   "Preview a session or directory",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 {
				return errors.New("session name or directory is required")
			}

			deps, err := buildDeps(cmd, base)
			if err != nil {
				return err
			}

			name := args[0]
			preview := deps.Previewer.Preview
			if contextual, ok := deps.Previewer.(previewer.ContextPreviewer); ok {
				preview = func(name string) (string, error) {
					return contextual.PreviewContext(cmd.Context(), name)
				}
			}
			watch, _ := cmd.Flags().GetBool("watch")
			interval, _ := cmd.Flags().GetDuration("interval")

			if !watch {
				return writePreview(cmd.OutOrStdout(), preview, name)
			}
			if interval <= 0 {
				return errors.New("preview interval must be greater than zero")
			}

			trimmedName := deps.Icon.RemoveIcon(name)
			if strings.HasPrefix(trimmedName, "ssh/") {
				return writePreview(cmd.OutOrStdout(), preview, name)
			}

			session, sessionExists := deps.Lister.FindTmuxSession(trimmedName)
			if !sessionExists {
				return writePreview(cmd.OutOrStdout(), preview, name)
			}

			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			watchPreviewFunc := func(string) (string, error) {
				return previewer.PreviewTmuxSession(deps.Tmux, session)
			}
			return watchPreview(cmd.Context(), cmd.OutOrStdout(), watchPreviewFunc, name, ticker.C)
		},
	}

	cmd.Flags().BoolP("watch", "w", false, "continuously refresh the preview")
	cmd.Flags().Duration("interval", 500*time.Millisecond, "refresh interval when watching")

	return cmd
}
