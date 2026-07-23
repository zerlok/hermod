package notify

import (
	"context"
	"runtime"

	"github.com/zerlok/hermod/internal/shell"
)

// soundFile is the local sound played alongside a Linux toast; a missing file just
// means no sound (best-effort).
const soundFile = "/usr/share/sounds/freedesktop/stereo/complete.oga"

// NewNotifier returns the Notifier for the running OS, running its tools through
// sh. An unsupported OS yields a no-op notifier — never an error. The concrete
// notifiers only build argv (no OS syscalls), so all of them compile and unit-test
// on any host; only this selector is platform-conditional.
func NewNotifier(sh shell.Shell) Notifier {
	switch runtime.GOOS {
	case "linux":
		return linuxNotifier{sh: sh}
	case "darwin":
		return darwinNotifier{sh: sh}
	default:
		return noopNotifier{}
	}
}

// linuxNotifier raises a toast via notify-send and plays a sound via paplay. The
// untrusted body is an argv element run with no intermediate shell, so it cannot
// inject a command; a `--` end-of-options guard stops a leading-dash title or body
// from being parsed as a notify-send option. Commands run with Capture set so their
// stdio is buffered (and discarded) rather than bleeding into the live attach
// terminal the notification fires over.
type linuxNotifier struct{ sh shell.Shell }

func (n linuxNotifier) Notify(ctx context.Context, m Message) error {
	_, _ = n.sh.Run(ctx, shell.Command{Capture: true, Argv: []string{
		"notify-send", "--app-name=hermod", "--urgency=" + urgency(m.Urgency),
		"--", orDefault(m.Title, "hermod"), m.Body,
	}})
	_, _ = n.sh.Run(ctx, shell.Command{Capture: true, Argv: []string{"paplay", soundFile}})
	return nil
}

// darwinNotifier raises a toast (with a sound) via osascript. The body and title
// are quoted into AppleScript string literals by asAppleStr. Capture keeps its
// stdio off the attach terminal.
type darwinNotifier struct{ sh shell.Shell }

func (n darwinNotifier) Notify(ctx context.Context, m Message) error {
	script := "display notification " + asAppleStr(m.Body) +
		" with title " + asAppleStr(orDefault(m.Title, "hermod")) +
		` sound name "Glass"`
	_, _ = n.sh.Run(ctx, shell.Command{Capture: true, Argv: []string{"osascript", "-e", script}})
	return nil
}

// noopNotifier is the notifier for unsupported platforms: it does nothing.
type noopNotifier struct{}

func (noopNotifier) Notify(context.Context, Message) error { return nil }
