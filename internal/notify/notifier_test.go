package notify

import (
	"context"
	"reflect"
	"testing"

	"github.com/zerlok/hermod/internal/shell"
)

// recAll records every command run through it, so a notifier that emits more than
// one command (toast + sound) can be asserted in full.
type recAll struct{ got [][]string }

func (r *recAll) Run(_ context.Context, cmd shell.Command) (shell.Result, error) {
	r.got = append(r.got, cmd.Argv)
	return shell.Result{}, nil
}

func TestLinuxNotifierArgv(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
		want [][]string
	}{
		{"defaults title and urgency", Message{Body: "done"}, [][]string{
			{"notify-send", "--app-name=hermod", "--urgency=normal", "--", "hermod", "done"},
			{"paplay", soundFile},
		}},
		{"explicit title and critical urgency", Message{Title: "agent", Body: "blocked", Urgency: "critical"}, [][]string{
			{"notify-send", "--app-name=hermod", "--urgency=critical", "--", "agent", "blocked"},
			{"paplay", soundFile},
		}},
		{"low urgency preserved", Message{Body: "x", Urgency: "low"}, [][]string{
			{"notify-send", "--app-name=hermod", "--urgency=low", "--", "hermod", "x"},
			{"paplay", soundFile},
		}},
		{"unknown urgency normalises to normal", Message{Body: "x", Urgency: "bogus"}, [][]string{
			{"notify-send", "--app-name=hermod", "--urgency=normal", "--", "hermod", "x"},
			{"paplay", soundFile},
		}},
		{"leading-dash title is not parsed as an option", Message{Title: "--icon=/etc/passwd", Body: "x"}, [][]string{
			{"notify-send", "--app-name=hermod", "--urgency=normal", "--", "--icon=/etc/passwd", "x"},
			{"paplay", soundFile},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recAll{}
			if err := (linuxNotifier{sh: rec}).Notify(context.Background(), tc.msg); err != nil {
				t.Fatalf("Notify() error: %v", err)
			}
			if !reflect.DeepEqual(rec.got, tc.want) {
				t.Errorf("argv = %q, want %q", rec.got, tc.want)
			}
		})
	}
}

func TestDarwinNotifierArgv(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
		want [][]string
	}{
		{"defaults title", Message{Body: "done"}, [][]string{
			{"osascript", "-e", `display notification "done" with title "hermod" sound name "Glass"`},
		}},
		{"quotes and backslashes escaped", Message{Title: `a"b`, Body: `he said "hi"`}, [][]string{
			{"osascript", "-e", `display notification "he said \"hi\"" with title "a\"b" sound name "Glass"`},
		}},
		{"control characters stripped", Message{Body: "line1\nline2"}, [][]string{
			{"osascript", "-e", `display notification "line1line2" with title "hermod" sound name "Glass"`},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recAll{}
			if err := (darwinNotifier{sh: rec}).Notify(context.Background(), tc.msg); err != nil {
				t.Fatalf("Notify() error: %v", err)
			}
			if !reflect.DeepEqual(rec.got, tc.want) {
				t.Errorf("argv = %q, want %q", rec.got, tc.want)
			}
		})
	}
}

func TestNoopNotifierRunsNothing(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
	}{
		{"any message is a no-op", Message{Title: "t", Body: "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := (noopNotifier{}).Notify(context.Background(), tc.msg); err != nil {
				t.Errorf("Notify() error = %v, want nil", err)
			}
		})
	}
}
