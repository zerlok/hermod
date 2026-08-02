package control

import (
	"context"
	"log"

	"github.com/zerlok/hermod/internal/notify"
	"github.com/zerlok/hermod/internal/sandbox"
	"github.com/zerlok/hermod/internal/shell"
)

// notifier is the run's handle on the notification back-channel: the channel the
// attach connection must carry, the environment that tells on-box senders where
// to write, and the local listener to shut down afterwards. The zero value is a
// run without notifications and every method on it is safe, so a caller never
// branches on whether the channel came up.
type notifier struct {
	channel sandbox.Channel
	stop    func() error
}

// Channel is the endpoint pair for the session to carry, zero when there is none.
func (n notifier) Channel() sandbox.Channel { return n.channel }

// Env is this notifier's contribution to the session environment — the caller
// combines it with the rest.
func (n notifier) Env() []string {
	if n.channel.IsZero() {
		return nil
	}
	return notify.Env(n.channel.Remote)
}

// Stop shuts the local listener down. Safe on the zero value and on repeat calls.
func (n notifier) Stop() error {
	if n.stop == nil {
		return nil
	}
	return n.stop()
}

// openNotify opens the notification back-channel, when the run wants one: it asks
// the sandbox for the channel and hands its local end to notify to serve. This is
// the one place the two halves meet — the sandbox knows nothing of notifications
// and notify knows nothing of sandboxes.
//
// It is the caller's job to keep this best-effort: an error means no channel, and
// the run is expected to log it and carry on.
func openNotify(ctx context.Context, real, effective shell.Shell, o Options, logger *log.Logger) (notifier, error) {
	if !o.Notify {
		return notifier{}, nil
	}
	if o.DryRun {
		// Opening the channel would provision an endpoint on the sandbox and bind a
		// local socket; a dry run does neither, so it plans a run without one.
		logger.Printf("# notify: back-channel not opened under --dry-run")
		return notifier{}, nil
	}
	channel, err := sandbox.OpenChannel(ctx, real, effective, o.Sandbox, notify.ChannelName)
	if err != nil {
		return notifier{}, err
	}
	stop, err := notify.Listen(ctx, channel.Local, notify.NewNotifier(effective), logger)
	if err != nil {
		return notifier{}, err
	}
	return notifier{channel: channel, stop: stop}, nil
}
