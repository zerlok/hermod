package shell

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/zerlok/hermod/internal/env"
	"github.com/zerlok/hermod/internal/secret"
)

// recExecutor records the command it was handed and runs nothing.
type recExecutor struct {
	got   Command
	calls int
}

func (r *recExecutor) Exec(_ context.Context, cmd Command) Execution {
	r.got = cmd
	r.calls++
	return Finished(cmd, nil)
}

func vars(pairs ...string) env.Vars {
	var out []env.Pair
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, env.Pair{Key: pairs[i], Value: env.Plain(pairs[i+1])})
	}
	return env.New(out...)
}

func secretVar(key, value string) env.Vars {
	return env.New(env.Pair{Key: key, Value: env.Secret(secret.FromString(value))})
}

func TestSSHRewrite(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		opts    []SSHOption
		in      Command
		want    []string
		wantErr bool
	}{
		{"probe no tty", "prod", nil, Command{Args: []string{"tmux", "has-session", "-t", "api"}}, []string{"ssh", "prod", "tmux has-session -t api"}, false},
		{"interactive tty", "prod", []SSHOption{WithTty()}, Command{Args: []string{"tmux", "new-session", "-A"}}, []string{"ssh", "-t", "prod", "tmux new-session -A"}, false},
		{"quotes remote arg with space", "box", nil, Command{Args: []string{"tmux", "new-session", "claude --model opus"}}, []string{"ssh", "box", "tmux new-session 'claude --model opus'"}, false},
		{
			"reverse forward after tty before host",
			"prod", []SSHOption{WithTty(), WithReverseForward(45001, 38211)},
			Command{Args: []string{"tmux", "attach"}},
			[]string{"ssh", "-t", "-R", "45001:127.0.0.1:38211", "prod", "tmux attach"},
			false,
		},
		{
			"own connection after tty before forwards",
			"prod", []SSHOption{WithReverseForward(45001, 38211), WithoutSharedMaster(), WithTty()},
			Command{Args: []string{"tmux", "attach"}},
			[]string{"ssh", "-t", "-o", "ControlPath=none", "-R", "45001:127.0.0.1:38211", "prod", "tmux attach"},
			false,
		},
		{
			"forward connection that exits when its forward fails",
			"prod", []SSHOption{WithReverseForward(45001, 38211), WithExitOnForwardFailure(), WithoutSharedMaster()},
			Command{Args: []string{"cat"}},
			[]string{"ssh", "-o", "ControlPath=none", "-o", "ExitOnForwardFailure=yes", "-R", "45001:127.0.0.1:38211", "prod", "cat"},
			false,
		},
		{"own connection probe", "prod", []SSHOption{WithoutSharedMaster()}, Command{Args: []string{"true"}}, []string{"ssh", "-o", "ControlPath=none", "prod", "true"}, false},
		{
			"two reverse forwards accumulate",
			"prod", []SSHOption{WithTty(), WithReverseForward(45001, 1), WithReverseForward(45002, 2)},
			Command{Args: []string{"tmux", "attach"}},
			[]string{"ssh", "-t", "-R", "45001:127.0.0.1:1", "-R", "45002:127.0.0.1:2", "prod", "tmux attach"},
			false,
		},
		{"remote dir folded into the remote line", "prod", nil, Command{Args: []string{"ls"}, Dir: "/home/u/my api"}, []string{"ssh", "prod", "cd '/home/u/my api' && ls"}, false},
		{"remote env folded into the remote line", "prod", nil, Command{Args: []string{"ls"}, Env: vars("A", "1", "B", "x y")}, []string{"ssh", "prod", "A=1 B='x y' ls"}, false},
		{"remote dir and env together", "prod", nil, Command{Args: []string{"ls"}, Dir: "/srv", Env: vars("A", "1")}, []string{"ssh", "prod", "cd /srv && A=1 ls"}, false},
		{"secret variable refused", "prod", nil, Command{Args: []string{"claude"}, Env: secretVar("ANTHROPIC_API_KEY", "sk-ant-secret")}, nil, true},
		{"remote dir without a command", "prod", nil, Command{Dir: "/srv"}, []string{"ssh", "prod", "cd /srv"}, false},
		{"remote env without a command", "prod", nil, Command{Env: vars("A", "1")}, []string{"ssh", "prod", "A=1"}, false},
		{"key with a ';' refused", "prod", nil, Command{Args: []string{"ls"}, Env: vars("A; rm -rf ~ #", "1")}, nil, true},
		{"key with a '.' refused", "prod", nil, Command{Args: []string{"ls"}, Env: vars("FOO.BAR", "1")}, nil, true},
		{"empty key refused", "prod", nil, Command{Args: []string{"ls"}, Env: vars("", "1")}, nil, true},
		{"key starting with a digit refused", "prod", nil, Command{Args: []string{"ls"}, Env: vars("1A", "1")}, nil, true},
		{"key of letters, digits and '_' kept", "prod", nil, Command{Args: []string{"ls"}, Env: vars("_a1_B", "1")}, []string{"ssh", "prod", "_a1_B=1 ls"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := &recExecutor{}
			err := NewSSH(inner, tc.host, tc.opts...).Exec(context.Background(), tc.in).Wait()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Wait() = %v, want an error: %v", err, tc.wantErr)
			}
			if tc.wantErr && inner.calls != 0 {
				t.Fatalf("refused command handed in: %q", inner.got.Args)
			}
			if !reflect.DeepEqual(inner.got.Args, tc.want) {
				t.Errorf("args = %q, want %q", inner.got.Args, tc.want)
			}
			if inner.got.Dir != "" || inner.got.Env.Len() != 0 {
				t.Errorf("remote intent leaked: dir=%q env=%v", inner.got.Dir, inner.got.Env)
			}
		})
	}
}

func TestTmuxRewrite(t *testing.T) {
	tags := []SessionOption{{"@hermod-mode", "attach"}, {"@hermod-dir", "dev/my api"}}
	cases := []struct {
		name    string
		sess    string
		opts    []TmuxOption
		in      Command
		want    []string
		wantErr bool
	}{
		{"default shell no command", "api", nil, Command{}, []string{"tmux", "new-session", "-A", "-s", "api"}, false},
		{"with dir", "api", nil, Command{Dir: "/home/u/api"}, []string{"tmux", "new-session", "-A", "-s", "api", "-c", "/home/u/api"}, false},
		{"a dir ending in ';' stays in its word", "api", nil, Command{Dir: "dev/a;"}, []string{"tmux", "new-session", "-A", "-s", "api", "-c", `dev/a\;`}, false},
		{"a '#' in the dir is no format", "api", nil, Command{Dir: "dev/p#(id>T)C#"}, []string{"tmux", "new-session", "-A", "-s", "api", "-c", "dev/p##(id>T)C##"}, false},
		{"with env", "api", nil, Command{Env: vars("GIT_AUTHOR_NAME", "Jane Doe", "A", "1")}, []string{"tmux", "new-session", "-A", "-s", "api", "-e", "GIT_AUTHOR_NAME=Jane Doe", "-e", "A=1"}, false},
		{"a value ending in ';' stays in its word", "api", nil, Command{Env: vars("A", "x;")}, []string{"tmux", "new-session", "-A", "-s", "api", "-e", `A=x\;`}, false},
		{"command words after --", "api", nil, Command{Dir: "/home/u/api", Args: []string{"claude", "--model", "opus"}}, []string{"tmux", "new-session", "-A", "-s", "api", "-c", "/home/u/api", "--", "claude", "--model", "opus"}, false},
		{"words ending in ';' stay words", "api", nil, Command{Args: []string{"sh", "-c", "a;", ";"}}, []string{"tmux", "new-session", "-A", "-s", "api", "--", "sh", "-c", `a\;`, `\;`}, false},
		{"a lone word quoted for tmux's shell", "api", nil, Command{Args: []string{"my prog;"}}, []string{"tmux", "new-session", "-A", "-s", "api", "--", "'my prog;'"}, false},
		{"secret variable refused", "api", nil, Command{Env: env.New(env.Pair{Key: "A", Value: env.Plain("1")}, env.Pair{Key: "ANTHROPIC_API_KEY", Value: env.Secret(secret.FromString("sk-ant-secret"))})}, nil, true},
		{"key with a space refused", "api", nil, Command{Env: vars("A B", "1")}, nil, true},
		{"key with a '=' refused", "api", nil, Command{Env: vars("A=B", "1")}, nil, true},
		{
			"session options follow the command",
			"api", []TmuxOption{WithSessionOptions(tags...)},
			Command{Dir: "dev/api", Args: []string{"htop"}},
			[]string{"tmux", "new-session", "-A", "-s", "api", "-c", "dev/api", "--", "htop", ";", "set-option", "-t", "=api:", "-qo", "@hermod-mode", "attach", ";", "set-option", "-t", "=api:", "-qo", "@hermod-dir", "dev/my api"},
			false,
		},
		{
			"detached start",
			"hermod-x", []TmuxOption{Detached(), WithSessionOptions(tags[0])},
			Command{Args: []string{"claude"}},
			[]string{"tmux", "new-session", "-d", "-s", "hermod-x", "--", "claude", ";", "set-option", "-t", "=hermod-x:", "-qo", "@hermod-mode", "attach"},
			false,
		},
		{
			"an option value ending in ';' stays in its word",
			"api", []TmuxOption{WithSessionOptions(SessionOption{"@hermod-x", "v;"})},
			Command{},
			[]string{"tmux", "new-session", "-A", "-s", "api", ";", "set-option", "-t", "=api:", "-qo", "@hermod-x", `v\;`},
			false,
		},
		{
			"an option value holding a tab refused",
			"api", []TmuxOption{WithSessionOptions(SessionOption{"@hermod-x", "a\tb"})},
			Command{},
			nil,
			true,
		},
		{
			"an option value holding a newline refused",
			"api", []TmuxOption{WithSessionOptions(SessionOption{"@hermod-x", "a\nb"})},
			Command{},
			nil,
			true,
		},
		{
			"an option that is no user option refused",
			"api", []TmuxOption{WithSessionOptions(SessionOption{"status", "off"})},
			Command{},
			nil,
			true,
		},
		{
			"options target the name tmux stores",
			"dev.api:2", []TmuxOption{WithSessionOptions(tags[0])},
			Command{},
			[]string{"tmux", "new-session", "-A", "-s", "dev.api:2", ";", "set-option", "-t", "=dev_api_2:", "-qo", "@hermod-mode", "attach"},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := &recExecutor{}
			err := NewTmux(inner, tc.sess, tc.opts...).Exec(context.Background(), tc.in).Wait()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Wait() = %v, want an error: %v", err, tc.wantErr)
			}
			if tc.wantErr && inner.calls != 0 {
				t.Fatalf("refused command handed in: %q", inner.got.Args)
			}
			if !reflect.DeepEqual(inner.got.Args, tc.want) {
				t.Errorf("args = %q, want %q", inner.got.Args, tc.want)
			}
			if inner.got.Dir != "" || inner.got.Env.Len() != 0 {
				t.Errorf("dir and env handed in as well: dir=%q env=%v", inner.got.Dir, inner.got.Env)
			}
		})
	}
}

func TestCheckSessionOption(t *testing.T) {
	cases := []struct {
		name    string
		option  SessionOption
		wantErr bool
	}{
		{"a user option", SessionOption{"@hermod-workspace", "hermod-box-api-1f2e"}, false},
		{"upper case and '_' in the name", SessionOption{"@Hermod_x", "v"}, false},
		{"a value with a space and ';'", SessionOption{"@w", "a b;"}, false},
		{"an empty value", SessionOption{"@w", ""}, false},
		{"no '@'", SessionOption{"hermod-origin", "x"}, true},
		{"a bare '@'", SessionOption{"@", "x"}, true},
		{"format syntax in the name", SessionOption{"@a}#{b", "x"}, true},
		{"a tab in the value", SessionOption{"@w", "a\tb"}, true},
		{"a newline in the value", SessionOption{"@w", "a\nb"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckSessionOption(tc.option); (err != nil) != tc.wantErr {
				t.Errorf("CheckSessionOption(%q) = %v, want an error: %v", tc.option, err, tc.wantErr)
			}
		})
	}
}

func TestTmuxSessionCommands(t *testing.T) {
	cases := []struct {
		name    string
		session string
		command func(TmuxSession) []string
		want    []string
	}{
		{"has", "hermod-k", TmuxSession.Has, []string{"sh", "-c", classifyScript, "tmux", "1", "2", "tmux", "has-session", "-t", "=hermod-k:"}},
		{"pane dead", "hermod-k", TmuxSession.PaneDead, []string{"sh", "-c", classifyScript, "tmux", "1", "2", "tmux", "has-session", "-t", "=hermod-k:", ";", "display-message", "-p", "-t", "=hermod-k:", "#{pane_dead}"}},
		{"kill", "hermod-k", TmuxSession.Kill, []string{"tmux", "kill-session", "-t", "=hermod-k:"}},
		{"attach", "hermod-k", TmuxSession.Attach, []string{"tmux", "attach-session", "-t", "=hermod-k:"}},
		{"keep a failed pane", "hermod-k", TmuxSession.KeepFailedPane, []string{"tmux", "set-option", "-w", "-t", "=hermod-k:", "remain-on-exit", "failed"}},
		{"the name tmux stores", "dev.api:2", TmuxSession.Kill, []string{"tmux", "kill-session", "-t", "=dev_api_2:"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.command(NewTmuxSession(tc.session)); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("command = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTmuxListingCommand(t *testing.T) {
	cases := []struct {
		name    string
		options []string
		want    []string
		wantErr bool
	}{
		{"no option", nil, []string{"sh", "-c", classifyScript, "tmux", "0", "2", "tmux", "list-sessions", "-F", "#{pane_dead}\t#{session_name}"}, false},
		{"two options", []string{"@hermod-workspace", "@hermod_origin"}, []string{"sh", "-c", classifyScript, "tmux", "0", "2", "tmux", "list-sessions", "-F", "#{pane_dead}\t#{@hermod-workspace}\t#{@hermod_origin}\t#{session_name}"}, false},
		{"no user option", []string{"status"}, nil, true},
		{"format syntax in a name", []string{"@a}#{b"}, nil, true},
		{"a bare '@'", []string{"@"}, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			listing, err := NewTmuxListing(tc.options...)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want an error: %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if got := listing.Command(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("command = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestClassify runs the classifying script under sh over a stand-in for tmux
// that prints and exits as each case says.
func TestClassify(t *testing.T) {
	cases := []struct {
		name       string
		absent     int
		out, says  string
		status     int
		wantStatus int
		wantOut    string
		wantErr    string
	}{
		{"success passes stdout through", TmuxAbsent, "1\n", "", 0, 0, "1\n", ""},
		{"a missing session", TmuxAbsent, "", "can't find session: hermod-k", 1, TmuxAbsent, "", ""},
		{"no server", TmuxAbsent, "", "no server running on /tmp/tmux-1000/default", 1, TmuxAbsent, "", ""},
		{"no socket directory", TmuxAbsent, "", "error connecting to /tmp/tmux-1000/default (No such file or directory)", 1, TmuxAbsent, "", ""},
		{"a listing with no server", 0, "", "no server running on /tmp/tmux-1000/default", 1, 0, "", ""},
		{"a socket it may not open", TmuxAbsent, "", "error connecting to /tmp/tmux-1000/default (Permission denied)", 1, TmuxUnreadable, "", "error connecting to /tmp/tmux-1000/default (Permission denied)\n"},
		{"any other failure", 0, "", "server exited unexpectedly", 1, TmuxUnreadable, "", "server exited unexpectedly\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stand := []string{"sh", "-c", `printf '%s' "$1"; printf '%s' "$2" >&2; exit "$3"`, "tmux", tc.out, tc.says, strconv.Itoa(tc.status)}
			args := classified(tc.absent, stand...)
			var out, errOut bytes.Buffer
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Stdout, cmd.Stderr = &out, &errOut
			err := cmd.Run()
			status := 0
			if exit, ok := err.(*exec.ExitError); ok {
				status = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if status != tc.wantStatus || out.String() != tc.wantOut || errOut.String() != tc.wantErr {
				t.Errorf("status %d, out %q, err %q; want %d, %q, %q", status, out.String(), errOut.String(), tc.wantStatus, tc.wantOut, tc.wantErr)
			}
		})
	}
}

func TestTmuxListingParse(t *testing.T) {
	cases := []struct {
		name    string
		options []string
		out     string
		want    []TmuxListed
		wantErr bool
	}{
		{"none", []string{"@w"}, "", nil, false},
		{
			"a running and a dead session",
			[]string{"@w"},
			"0\tws\thermod-a\n1\t\thermod-b\n",
			[]TmuxListed{
				{Name: "hermod-a", Options: map[string]string{"@w": "ws"}},
				{Name: "hermod-b", Dead: true, Options: map[string]string{"@w": ""}},
			},
			false,
		},
		{
			"a name holding the separator, and no final newline",
			nil,
			"0\tmy\tsession",
			[]TmuxListed{{Name: "my\tsession", Options: map[string]string{}}},
			false,
		},
		{
			"two options in their order",
			[]string{"@w", "@o"},
			"0\tws\tbox\thermod-a\n",
			[]TmuxListed{{Name: "hermod-a", Options: map[string]string{"@w": "ws", "@o": "box"}}},
			false,
		},
		{"a line short of a field", []string{"@w"}, "0\thermod-a\n", nil, true},
		{"an unreadable pane state", nil, "x\thermod-a\n", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			listing, err := NewTmuxListing(tc.options...)
			if err != nil {
				t.Fatal(err)
			}
			got, err := listing.Parse(tc.out)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, want an error: %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("sessions = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestLine runs each line through sh, which must read back the words it was
// made from.
func TestLine(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"safe words", []string{"tmux", "has-session", "-t", "=hermod-k:"}, "tmux has-session -t =hermod-k:"},
		{"a format", []string{"tmux", "display-message", "-p", "#{pane_dead}"}, "tmux display-message -p '#{pane_dead}'"},
		{"a quote and a space", []string{"echo", "it's here"}, `echo 'it'\''s here'`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := Line(tc.args)
			if line != tc.want {
				t.Errorf("Line = %q, want %q", line, tc.want)
			}
			out, err := exec.Command("sh", "-c", `eval "set -- $1"; printf '%s\n' "$@"`, "sh", line).Output()
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n"); !reflect.DeepEqual(got, tc.args) {
				t.Errorf("sh read %q, want %q", got, tc.args)
			}
		})
	}
}

func TestLoginRewrite(t *testing.T) {
	cases := []struct {
		name    string
		in      Command
		want    []string
		wantDir string
	}{
		{"empty command untouched", Command{Dir: "/home/u/api"}, nil, "/home/u/api"},
		{"single word wrapped", Command{Args: []string{"claude"}}, []string{"sh", "-c", `exec "$SHELL" -lc "$1"`, "sh", "claude"}, ""},
		{"flags preserved in one word", Command{Args: []string{"claude", "--model", "opus"}}, []string{"sh", "-c", `exec "$SHELL" -lc "$1"`, "sh", "claude --model opus"}, ""},
		{"inner spaces stay quoted", Command{Args: []string{"claude", "-p", "hi there"}}, []string{"sh", "-c", `exec "$SHELL" -lc "$1"`, "sh", "claude -p 'hi there'"}, ""},
		{"dir kept for the wrapper inside", Command{Dir: "dev/api", Args: []string{"claude"}}, []string{"sh", "-c", `exec "$SHELL" -lc "$1"`, "sh", "claude"}, "dev/api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := &recExecutor{}
			NewLogin(inner).Exec(context.Background(), tc.in)
			if !reflect.DeepEqual(inner.got.Args, tc.want) {
				t.Errorf("args = %q, want %q", inner.got.Args, tc.want)
			}
			if inner.got.Dir != tc.wantDir {
				t.Errorf("dir = %q, want %q", inner.got.Dir, tc.wantDir)
			}
		})
	}
}

// TestNesting asserts each stack of wrappers hands one command line to the
// leaf, and that a dry-run leaf under the same stack prints that line.
func TestNesting(t *testing.T) {
	cases := []struct {
		name    string
		stack   func(leaf Executor) Executor
		in      Command
		want    []string
		plan    string
		wantErr bool
	}{
		{
			"tmux over ssh, no command",
			func(leaf Executor) Executor { return NewTmux(NewSSH(leaf, "prod", WithTty()), "api") },
			Command{Dir: "/home/u/api"},
			[]string{"ssh", "-t", "prod", "tmux new-session -A -s api -c /home/u/api"},
			"ssh -t prod 'tmux new-session -A -s api -c /home/u/api'\n",
			false,
		},
		{
			"tmux over ssh with identity",
			func(leaf Executor) Executor { return NewTmux(NewSSH(leaf, "prod", WithTty()), "api") },
			Command{Dir: "/home/u/api", Env: vars("GIT_AUTHOR_NAME", "Jane Doe"), Args: []string{"claude", "--model", "opus"}},
			[]string{"ssh", "-t", "prod", "tmux new-session -A -s api -c /home/u/api -e 'GIT_AUTHOR_NAME=Jane Doe' -- claude --model opus"},
			`ssh -t prod 'tmux new-session -A -s api -c /home/u/api -e '\''GIT_AUTHOR_NAME=Jane Doe'\'' -- claude --model opus'` + "\n",
			false,
		},
		{
			"login over tmux over ssh",
			func(leaf Executor) Executor { return NewLogin(NewTmux(NewSSH(leaf, "prod"), "api", Detached())) },
			Command{Args: []string{"claude"}},
			[]string{"ssh", "prod", `tmux new-session -d -s api -- sh -c 'exec "$SHELL" -lc "$1"' sh claude`},
			`ssh prod 'tmux new-session -d -s api -- sh -c '\''exec "$SHELL" -lc "$1"'\'' sh claude'` + "\n",
			false,
		},
		{
			"login over ssh",
			func(leaf Executor) Executor { return NewLogin(NewSSH(leaf, "prod")) },
			Command{Args: []string{"true"}},
			[]string{"ssh", "prod", `sh -c 'exec "$SHELL" -lc "$1"' sh true`},
			`ssh prod 'sh -c '\''exec "$SHELL" -lc "$1"'\'' sh true'` + "\n",
			false,
		},
		{
			"ssh with remote dir",
			func(leaf Executor) Executor { return NewSSH(leaf, "prod") },
			Command{Dir: "/home/u/api", Args: []string{"ls"}},
			[]string{"ssh", "prod", "cd /home/u/api && ls"},
			"ssh prod 'cd /home/u/api && ls'\n",
			false,
		},
		{
			"login over ssh with dir and env",
			func(leaf Executor) Executor { return NewLogin(NewSSH(leaf, "prod")) },
			Command{Dir: "/srv", Env: vars("A", "1"), Args: []string{"true"}},
			[]string{"ssh", "prod", `cd /srv && A=1 sh -c 'exec "$SHELL" -lc "$1"' sh true`},
			`ssh prod 'cd /srv && A=1 sh -c '\''exec "$SHELL" -lc "$1"'\'' sh true'` + "\n",
			false,
		},
		{
			"tmux over ssh refuses a secret",
			func(leaf Executor) Executor { return NewTmux(NewSSH(leaf, "prod", WithTty()), "api") },
			Command{Env: secretVar("ANTHROPIC_API_KEY", "sk-ant-secret"), Args: []string{"claude"}},
			nil,
			"",
			true,
		},
		{
			"login over ssh with dir, no command",
			func(leaf Executor) Executor { return NewLogin(NewSSH(leaf, "prod")) },
			Command{Dir: "/srv"},
			[]string{"ssh", "prod", "cd /srv"},
			"ssh prod 'cd /srv'\n",
			false,
		},
		{
			"login over ssh refuses a bad key",
			func(leaf Executor) Executor { return NewLogin(NewSSH(leaf, "prod")) },
			Command{Env: vars("A;B", "1"), Args: []string{"ls"}},
			nil,
			"",
			true,
		},
		{
			"tmux over ssh refuses a bad key",
			func(leaf Executor) Executor { return NewTmux(NewSSH(leaf, "prod"), "api") },
			Command{Env: vars("A B", "1")},
			nil,
			"",
			true,
		},
		{
			"login over ssh refuses a secret",
			func(leaf Executor) Executor { return NewLogin(NewSSH(leaf, "prod")) },
			Command{Env: secretVar("ANTHROPIC_API_KEY", "sk-ant-secret"), Args: []string{"claude"}},
			nil,
			"",
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recExecutor{}
			if err := tc.stack(rec).Exec(context.Background(), tc.in).Wait(); (err != nil) != tc.wantErr {
				t.Fatalf("Wait() = %v, want an error: %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(rec.got.Args, tc.want) {
				t.Errorf("args = %q, want %q", rec.got.Args, tc.want)
			}
			var plan bytes.Buffer
			if err := tc.stack(NewDryRun(&plan)).Exec(context.Background(), tc.in).Wait(); (err != nil) != tc.wantErr {
				t.Fatalf("dry run: %v, want an error: %v", err, tc.wantErr)
			}
			if plan.String() != tc.plan {
				t.Errorf("plan = %q, want %q", plan.String(), tc.plan)
			}
		})
	}
}

func TestIsName(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"PATH", true},
		{"_private", true},
		{"a1_B2", true},
		{"", false},
		{"1A", false},
		{"FOO.BAR", false},
		{"A;B", false},
		{"A-B", false},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			if got := IsName(tc.key); got != tc.want {
				t.Errorf("IsName(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

func TestFormat(t *testing.T) {
	cases := []struct {
		name string
		cmd  Command
		want string
	}{
		{"plain args", Command{Args: []string{"mutagen", "sync", "list"}}, "mutagen sync list"},
		{"quotes arg with space", Command{Args: []string{"echo", "hello world"}}, "echo 'hello world'"},
		{"env prefix quoted value", Command{Args: []string{"git", "commit"}, Env: vars("GIT_AUTHOR_NAME", "Jane Doe")}, "GIT_AUTHOR_NAME='Jane Doe' git commit"},
		{"safe path unquoted", Command{Args: []string{"ls"}, Dir: "/home/u/api"}, "cd /home/u/api && ls"},
		{"dir env and args", Command{Args: []string{"git", "commit"}, Dir: "/home/u/api", Env: vars("K", "v")}, "cd /home/u/api && K=v git commit"},
		{"single quote in value", Command{Args: []string{"echo", "it's"}}, `echo 'it'\''s'`},
		{
			"secret masked",
			Command{Args: []string{"claude"}, Env: env.New(env.Pair{Key: "ANTHROPIC_API_KEY", Value: env.Secret(secret.FromString("sk-ant-secret"))})},
			"ANTHROPIC_API_KEY='secret.Token(redacted)' claude",
		},
		{"dir without a command", Command{Dir: "/srv"}, "cd /srv"},
		{"env without a command", Command{Env: vars("A", "1")}, "A=1"},
		{"dir and env without a command", Command{Dir: "/srv", Env: vars("A", "1")}, "cd /srv && A=1"},
		{"empty", Command{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := format(tc.cmd); got != tc.want {
				t.Errorf("format() = %q, want %q", got, tc.want)
			}
		})
	}
}

// failingWriter fails every write.
type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestDryRunPrintsAndRunsNothing(t *testing.T) {
	errPlan := errors.New("plan closed")
	cases := []struct {
		name    string
		plan    io.Writer
		cmd     Command
		want    string
		wantErr error
	}{
		{"side effect printed", &bytes.Buffer{}, Command{Args: []string{"mutagen", "sync", "terminate", "api"}}, "mutagen sync terminate api\n", nil},
		{"attach printed not run", &bytes.Buffer{}, Command{Args: []string{"ssh", "-t", "prod", "tmux", "attach"}}, "ssh -t prod tmux attach\n", nil},
		{"plan that fails is the result", failingWriter{errPlan}, Command{Args: []string{"true"}}, "", errPlan},
		{"key that is no shell name refused, nothing printed", &bytes.Buffer{}, Command{Args: []string{"ls"}, Env: vars("A;B", "1")}, "", errNotAName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex := NewDryRun(tc.plan).Exec(context.Background(), tc.cmd)
			if _, err := ex.Stdin.Write([]byte("ignored")); err != nil {
				t.Errorf("stdin write: %v", err)
			}
			if out, _ := io.ReadAll(ex.Stdout); len(out) != 0 {
				t.Errorf("stdout = %q, want empty (nothing executed)", out)
			}
			if err := ex.Wait(); !errors.Is(err, tc.wantErr) {
				t.Errorf("Wait() = %v, want %v", err, tc.wantErr)
			}
			if buf, ok := tc.plan.(*bytes.Buffer); ok && buf.String() != tc.want {
				t.Errorf("printed %q, want %q", buf.String(), tc.want)
			}
		})
	}
}

func TestRunCollects(t *testing.T) {
	cases := []struct {
		name       string
		cmd        Command
		wantStdout string
		wantStderr string
		wantCode   int // -1: no exit code, the command succeeded
	}{
		{"printf no newline", Command{Args: []string{"printf", "hello"}}, "hello", "", -1},
		{"echo with newline", Command{Args: []string{"echo", "sync-ok"}}, "sync-ok\n", "", -1},
		{"stderr collected separately", Command{Args: []string{"sh", "-c", "echo out; echo err >&2"}}, "out\n", "err\n", -1},
		{"variables added", Command{Args: []string{"sh", "-c", "echo $HERMOD_TEST"}, Env: vars("HERMOD_TEST", "carried")}, "carried\n", "", -1},
		{"secret variable revealed to the process", Command{Args: []string{"sh", "-c", "echo $HERMOD_TEST"}, Env: env.New(env.Pair{Key: "HERMOD_TEST", Value: env.Secret(secret.FromString("hidden"))})}, "hidden\n", "", -1},
		{"directory", Command{Args: []string{"pwd"}, Dir: "/"}, "/\n", "", -1},
		{"no stdin reads empty", Command{Args: []string{"cat"}}, "", "", -1},
		{"given stdin read", Command{Args: []string{"cat"}, Stdin: strings.NewReader("given")}, "given", "", -1},
		{"exit code", Command{Args: []string{"sh", "-c", "echo bad >&2; exit 7"}}, "", "bad\n", 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Run(context.Background(), NewLocal(), tc.cmd)
			code, ok := ExitCode(err)
			if !ok {
				code = -1
			}
			if code != tc.wantCode || (code == -1 && err != nil) {
				t.Errorf("err = %v (code %d), want code %d", err, code, tc.wantCode)
			}
			if out != (Output{Stdout: tc.wantStdout, Stderr: tc.wantStderr}) {
				t.Errorf("Output = %+v, want stdout %q, stderr %q", out, tc.wantStdout, tc.wantStderr)
			}
		})
	}
}

// TestExecutionPipes asserts the streams a Command leaves nil are pipes on its
// Execution, and the streams it gives are used as they are.
func TestExecutionPipes(t *testing.T) {
	cases := []struct {
		name       string
		script     string
		input      string
		giveStdout bool
		sequential bool // stdout read to its end before stderr is read
		wantStdout string
		wantStderr string
	}{
		{"stdin pipe to stdout pipe", "cat", "hello\n", false, false, "hello\n", ""},
		{"stdin pipe to stderr pipe", "cat >&2", "line\n", false, false, "", "line\n"},
		{"stdout given, stderr piped", "echo out; echo err >&2", "", true, false, "out\n", "err\n"},
		{"each pipe ends on its own", "echo out; echo err >&2", "", false, true, "out\n", "err\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var given bytes.Buffer
			cmd := Command{Args: []string{"sh", "-c", tc.script}}
			if tc.giveStdout {
				cmd.Stdout = &given
			}
			ex := NewLocal().Exec(context.Background(), cmd)
			if (ex.Stdout == nil) != tc.giveStdout {
				t.Fatalf("Stdout pipe = %v, want one only for a nil stream", ex.Stdout)
			}
			if _, err := io.WriteString(ex.Stdin, tc.input); err != nil {
				t.Fatalf("write stdin: %v", err)
			}
			if err := ex.Stdin.Close(); err != nil {
				t.Fatalf("close stdin: %v", err)
			}
			var stdout, stderr []byte
			readStdout := func() {
				if ex.Stdout != nil {
					stdout, _ = io.ReadAll(ex.Stdout)
				}
			}
			readStderr := func() { stderr, _ = io.ReadAll(ex.Stderr) }
			var wg sync.WaitGroup
			if tc.sequential {
				wg.Go(func() { readStdout(); readStderr() })
			} else {
				wg.Go(readStdout)
				wg.Go(readStderr)
			}
			if !waitWithin(&wg, 2*time.Second) {
				t.Fatal("pipes did not end")
			}
			if err := ex.Wait(); err != nil {
				t.Fatalf("Wait() = %v", err)
			}
			if tc.giveStdout {
				stdout = given.Bytes()
			}
			if string(stdout) != tc.wantStdout || string(stderr) != tc.wantStderr {
				t.Errorf("stdout, stderr = %q, %q, want %q, %q", stdout, stderr, tc.wantStdout, tc.wantStderr)
			}
		})
	}
}

// TestExecutionThatNeverRan asserts a command that cannot start, and one no
// Executor returned, report an error, and the pipes of one that cannot start
// end at once.
func TestExecutionThatNeverRan(t *testing.T) {
	cases := []struct {
		name string
		ex   func() Execution
	}{
		{"no program", func() Execution { return NewLocal().Exec(context.Background(), Command{}) }},
		{"program not found", func() Execution {
			return NewLocal().Exec(context.Background(), Command{Args: []string{"/nonexistent/hermod-test-program"}})
		}},
		{"zero execution", func() Execution { return Execution{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex := tc.ex()
			for _, r := range []io.Reader{ex.Stdout, ex.Stderr} {
				if r == nil {
					continue
				}
				if out, err := io.ReadAll(r); len(out) != 0 || err != nil {
					t.Errorf("pipe = %q, %v, want empty", out, err)
				}
			}
			if err := ex.Wait(); err == nil {
				t.Error("Wait() = nil, want an error")
			}
		})
	}
}

func TestExitCode(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantOK   bool
	}{
		{"no error", nil, 0, false},
		{"not a process error", errors.New("plain"), 0, false},
		{"process exit", exitError(3), 3, true},
		{"wrapped process exit", errors.Join(errors.New("context"), exitError(5)), 5, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, ok := ExitCode(tc.err)
			if code != tc.wantCode || ok != tc.wantOK {
				t.Errorf("ExitCode() = %d, %v, want %d, %v", code, ok, tc.wantCode, tc.wantOK)
			}
		})
	}
}

// exitError carries an exit code, as a fake Executor's result may.
type exitError int

func (e exitError) Error() string { return "exit" }
func (e exitError) ExitCode() int { return int(e) }

// TestWrappersCarryStreams asserts every wrapper hands the command's streams
// and interrupt through to the leaf, so a stream given at the top of a stack
// is the one the child runs with.
func TestWrappersCarryStreams(t *testing.T) {
	cases := []struct {
		name  string
		stack func(inner Executor) Executor
	}{
		{"ssh", func(in Executor) Executor { return NewSSH(in, "prod") }},
		{"tmux", func(in Executor) Executor { return NewTmux(in, "api") }},
		{"login", func(in Executor) Executor { return NewLogin(in) }},
		{"login over ssh", func(in Executor) Executor { return NewLogin(NewSSH(in, "prod")) }},
		{"full attach stack", func(in Executor) Executor { return NewLogin(NewTmux(NewSSH(in, "prod", WithTty()), "api")) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdin := strings.NewReader("in")
			var stdout, stderr bytes.Buffer
			cmd := Command{Args: []string{"claude", "auth", "status"}, Stdin: stdin, Stdout: &stdout, Stderr: &stderr, Interrupt: syscall.SIGTERM}
			inner := &recExecutor{}
			tc.stack(inner).Exec(context.Background(), cmd)
			if inner.got.Stdin != cmd.Stdin || inner.got.Stdout != cmd.Stdout || inner.got.Stderr != cmd.Stderr {
				t.Errorf("streams = %v, %v, %v, want %v, %v, %v",
					inner.got.Stdin, inner.got.Stdout, inner.got.Stderr, cmd.Stdin, cmd.Stdout, cmd.Stderr)
			}
			if inner.got.Interrupt != cmd.Interrupt {
				t.Errorf("Interrupt = %v, want %v", inner.got.Interrupt, cmd.Interrupt)
			}
		})
	}
}

func TestNoInputIsEmpty(t *testing.T) {
	cases := []struct {
		name string
		buf  int
		want error
	}{
		{"empty read", 0, io.EOF},
		{"buffered read", 64, io.EOF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, err := NoInput.Read(make([]byte, tc.buf))
			if n != 0 || err != tc.want {
				t.Errorf("Read() = %d, %v, want 0, %v", n, err, tc.want)
			}
		})
	}
}

// TestInterrupt asserts a cancelled command given an Interrupt gets that
// signal and the time to act on it, and one without is killed at once.
func TestInterrupt(t *testing.T) {
	cases := []struct {
		name       string
		script     string
		interrupt  os.Signal
		wantStdout string
		wantCode   int // -1: killed by a signal
	}{
		{"interrupted", `trap 'echo interrupted; exit 3' TERM; echo ready; while :; do sleep 0.05; done`, syscall.SIGTERM, "ready\ninterrupted\n", 3},
		{"killed", `trap 'echo interrupted; exit 3' TERM; echo ready; while :; do sleep 0.05; done`, nil, "ready\n", -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			out := &signalOnWrite{ready: make(chan struct{})}
			go func() {
				<-out.ready
				cancel()
			}()
			err := NewLocal().Exec(ctx, Command{Args: []string{"sh", "-c", tc.script}, Stdin: NoInput, Stdout: out, Stderr: io.Discard, Interrupt: tc.interrupt}).Wait()
			code, ok := ExitCode(err)
			if !ok || code != tc.wantCode {
				t.Errorf("exit = %v (%d), want code %d", err, code, tc.wantCode)
			}
			if got := out.String(); got != tc.wantStdout {
				t.Errorf("stdout = %q, want %q", got, tc.wantStdout)
			}
		})
	}
}

// signalOnWrite is a buffer that closes ready on its first write.
type signalOnWrite struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	ready chan struct{}
	once  sync.Once
}

func (w *signalOnWrite) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	defer w.once.Do(func() { close(w.ready) })
	return w.buf.Write(p)
}

func (w *signalOnWrite) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestReturnsDespiteALeftChild asserts a command returns soon after it ends or
// is cancelled even while a child it left behind holds its output open, and
// that a command which succeeded still reports success.
func TestReturnsDespiteALeftChild(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		piped   bool
		timeout time.Duration // 0: never cancelled
		within  time.Duration // well under the left child's life
		wantErr bool
	}{
		{"cancelled, collected", []string{"sh", "-c", "sleep 3 & sleep 3"}, false, 100 * time.Millisecond, 2 * time.Second, true},
		{"cancelled, piped", []string{"sh", "-c", "sleep 3 & sleep 3"}, true, 100 * time.Millisecond, 2 * time.Second, true},
		{"succeeded, collected", []string{"sh", "-c", "sleep 3 & echo done"}, false, 0, 2 * time.Second, false},
		{"succeeded, piped", []string{"sh", "-c", "sleep 3 & echo done"}, true, 0, 2 * time.Second, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.timeout)
				defer cancel()
			}
			cmd := Command{Args: tc.args, Stdin: NoInput}
			start := time.Now()
			var err error
			if tc.piped {
				ex := NewLocal().Exec(ctx, cmd)
				var wg sync.WaitGroup
				wg.Go(func() { _, _ = io.Copy(io.Discard, ex.Stdout) })
				wg.Go(func() { _, _ = io.Copy(io.Discard, ex.Stderr) })
				wg.Wait()
				err = ex.Wait()
			} else {
				_, err = Run(ctx, NewLocal(), cmd)
			}
			if took := time.Since(start); took > tc.within {
				t.Errorf("returned after %v, want within %v", took, tc.within)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("error = %v, want one: %v", err, tc.wantErr)
			}
		})
	}
}

// waitWithin reports whether wg is done before d passes.
func waitWithin(wg *sync.WaitGroup, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(d):
		return false
	}
}

// TestCancelledWithUnreadPipes asserts a cancelled command returns, and its
// pipes end, even though the caller stopped reading one of them while the
// command was still writing to it.
func TestCancelledWithUnreadPipes(t *testing.T) {
	cases := []struct {
		name       string
		script     string // writes more to stderr than a pipe holds
		readStdout bool
		within     time.Duration // well under the command's own life
	}{
		{"neither pipe read", "head -c 1000000 /dev/zero >&2; sleep 5", false, 2 * time.Second},
		{"only stdout read", "echo out; head -c 1000000 /dev/zero >&2; sleep 5", true, 2 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			ex := NewLocal().Exec(ctx, Command{Args: []string{"sh", "-c", tc.script}, Stdin: NoInput})
			var wg sync.WaitGroup
			wg.Go(func() {
				if tc.readStdout {
					_, _ = io.Copy(io.Discard, ex.Stdout)
				}
				_ = ex.Wait()
			})
			if !waitWithin(&wg, tc.within) {
				t.Fatalf("Wait did not return within %v", tc.within)
			}
			wg.Go(func() { _, _ = io.Copy(io.Discard, ex.Stderr) })
			if !waitWithin(&wg, tc.within-time.Since(start)) {
				t.Errorf("the unread pipe did not end within %v", tc.within)
			}
		})
	}
}
