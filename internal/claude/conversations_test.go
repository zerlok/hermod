package claude

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zerlok/hermod/internal/agent"
)

const (
	other     = "7a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	runningID = "1f2e3d4c-5b6a-4978-8695-a4b3c2d1e0f9"
)

// answer is the script's report: its lines, then the conversations
// Claude runs.
func answer(agentsJSON string, lines ...string) []byte {
	var b strings.Builder
	b.WriteString("conversations:\n")
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	b.WriteString("agents:\n" + agentsJSON + "\n")
	return []byte(b.String())
}

// errFailed is a command's failure.
var errFailed = errors.New("exit status 1")

// errAny stands, in a table, for any error.
var errAny = errors.New("any error")

func matches(err, want error) bool {
	if want == errAny {
		return err != nil
	}
	if want == nil {
		return err == nil
	}
	return errors.Is(err, want)
}

// profile is what a login profile prints before a command's own output.
const profile = "Welcome to the workstation\nagents:\n"

func at(seconds int64) time.Time { return time.Unix(seconds, 0).UTC() }

func TestList(t *testing.T) {
	cases := []struct {
		name        string
		dir         string
		out         []byte
		fails       error // the command's result
		wantCommand []string
		want        []agent.Conversation
		wantErr     bool
	}{
		{
			name: "a Stopped conversation with a transcript, not listed by claude agents",
			dir:  "/home/u/dev/api",
			out: answer(`[]`,
				"transcript /home/u/.claude/projects/-home-u-dev-api/"+uuid+".jsonl",
				"updated 1794082248",
				`cwd "cwd":"/home/u/dev/api"`,
				`ai-title "aiTitle":"Fix the \"login\" flow"`,
			),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "list", "-home-u-dev-api"},
			want:        []agent.Conversation{{ID: uuid, Dir: "/home/u/dev/api", State: agent.Stopped, Title: `Fix the "login" flow`, Updated: at(1794082248)}},
		},
		{
			name: "the user's title over Claude's, a subdirectory kept, a sibling and another project left out",
			dir:  "/home/u/dev/api/",
			out: answer(`[]`,
				"transcript /c/projects/-home-u-dev-api-lib/"+uuid+".jsonl",
				"updated 20",
				`cwd "cwd":"/home/u/dev/api/lib"`,
				`custom-title "customTitle":"mine"`,
				`ai-title "aiTitle":"Claude's"`,
				"transcript /c/projects/-home-u-dev-api2/"+other+".jsonl",
				"updated 30",
				`cwd "cwd":"/home/u/dev/api2"`,
				"transcript /c/projects/-home-u-dev-api-v2/"+fork+".jsonl",
				"updated 40",
				`cwd "cwd":"/home/u/dev/api.v2"`,
			),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "list", "-home-u-dev-api"},
			want:        []agent.Conversation{{ID: uuid, Dir: "/home/u/dev/api/lib", State: agent.Stopped, Title: "mine", Updated: at(20)}},
		},
		{
			name: "Running ones, with and without a transcript, newest first; one run elsewhere left out",
			dir:  "/home/u/dev/api",
			out: answer(`[{"sessionId":"`+uuid+`","cwd":"/home/u/dev/api","name":"from agents","startedAt":5000},`+
				`{"sessionId":"`+runningID+`","cwd":"/home/u/dev/api","name":"new","startedAt":90000},`+
				`{"sessionId":"`+other+`","cwd":"/srv/elsewhere","name":"elsewhere","startedAt":1000}]`,
				"transcript /c/projects/-home-u-dev-api/"+uuid+".jsonl",
				"updated 50",
				`cwd "cwd":"/home/u/dev/api"`,
			),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "list", "-home-u-dev-api"},
			want: []agent.Conversation{
				{ID: runningID, Dir: "/home/u/dev/api", State: agent.Running, Title: "new", Updated: at(90)},
				{ID: uuid, Dir: "/home/u/dev/api", State: agent.Running, Title: "from agents", Updated: at(50)},
			},
		},
		{
			name: "a transcript with no directory or no UUID name left out, a newer copy kept",
			dir:  "/home/u/dev/api",
			out: answer(`[]`,
				"transcript /c/projects/-home-u-dev-api/"+other+".jsonl",
				"updated 10",
				"transcript /c/projects/-home-u-dev-api/notes.jsonl",
				"updated 10",
				`cwd "cwd":"/home/u/dev/api"`,
				"transcript /c/projects/-home-u-dev-api/"+uuid+".jsonl",
				"updated 10",
				`cwd "cwd":"/home/u/dev/api"`,
				"transcript /c/projects/-home-u-dev-api-copy/"+uuid+".jsonl",
				"updated 70",
				`cwd "cwd":"/home/u/dev/api/copy"`,
				"transcript /c/projects/-home-u-dev-api-x/"+fork+".jsonl",
				"updated",
				`cwd "cwd":"/home/u/dev/api/../x"`,
			),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "list", "-home-u-dev-api"},
			want:        []agent.Conversation{{ID: uuid, Dir: "/home/u/dev/api/copy", State: agent.Stopped, Updated: at(70)}},
		},
		{
			name: "begun locally and resumed here: the directory named after its project",
			dir:  "/home/u/dev/api",
			out: answer(`[]`,
				"transcript /c/projects/-home-u-dev-api/"+uuid+".jsonl",
				"updated 10",
				`cwd "cwd":"/Users/me/dev/api"`,
				`cwd "cwd":"/home/u/dev/api"`,
			),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "list", "-home-u-dev-api"},
			want:        []agent.Conversation{{ID: uuid, Dir: "/home/u/dev/api", State: agent.Stopped, Updated: at(10)}},
		},
		{
			name: "begun locally, not yet resumed here: the directory asked for, its project the transcript's",
			dir:  "/home/u/dev/api",
			out: answer(`[]`,
				"transcript /c/projects/-home-u-dev-api/"+uuid+".jsonl",
				"updated 10",
				`cwd "cwd":"/Users/me/dev/api"`,
				"transcript /c/projects/-home-u-dev-api-lib/"+other+".jsonl",
				"updated 20",
				`cwd "cwd":"/Users/me/dev/api/lib"`,
			),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "list", "-home-u-dev-api"},
			want:        []agent.Conversation{{ID: uuid, Dir: "/home/u/dev/api", State: agent.Stopped, Updated: at(10)}},
		},
		{
			name:        "none",
			dir:         "/",
			out:         answer(`[]`),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "list", "-"},
		},
		{
			name:        "no list of runningID conversations",
			dir:         "/home/u/dev/api",
			out:         []byte("transcript /c/projects/x/" + uuid + ".jsonl\n"),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "list", "-home-u-dev-api"},
			wantErr:     true,
		},
		{
			name: "lines a login profile printed first",
			dir:  "/home/u/dev/api",
			out: append([]byte(profile), answer(`[]`,
				"transcript /c/projects/-home-u-dev-api/"+uuid+".jsonl",
				"updated 10",
				`cwd "cwd":"/home/u/dev/api"`,
			)...),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "list", "-home-u-dev-api"},
			want:        []agent.Conversation{{ID: uuid, Dir: "/home/u/dev/api", State: agent.Stopped, Updated: at(10)}},
		},
		{
			name:        "no report",
			dir:         "/home/u/dev/api",
			out:         []byte(profile + "[]\n"),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "list", "-home-u-dev-api"},
			wantErr:     true,
		},
		{
			name:        "claude agents failed",
			dir:         "/home/u/dev/api",
			out:         answer("error: unknown command 'agents'"),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "list", "-home-u-dev-api"},
			wantErr:     true,
		},
		{
			name:        "a line the script does not write",
			dir:         "/home/u/dev/api",
			out:         answer(`[]`, "transcript /c/projects/x/"+uuid+".jsonl", "size 12"),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "list", "-home-u-dev-api"},
			wantErr:     true,
		},
		{
			name:        "a line before any transcript",
			dir:         "/home/u/dev/api",
			out:         answer(`[]`, `cwd "cwd":"/home/u/dev/api"`),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "list", "-home-u-dev-api"},
			wantErr:     true,
		},
		{
			name:        "a malformed member",
			dir:         "/home/u/dev/api",
			out:         answer(`[]`, "transcript /c/projects/x/"+uuid+".jsonl", `cwd "cwd":"/home/u`),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "list", "-home-u-dev-api"},
			wantErr:     true,
		},
		{
			name:        "an unreadable last change",
			dir:         "/home/u/dev/api",
			out:         answer(`[]`, "transcript /c/projects/x/"+uuid+".jsonl", "updated yesterday"),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "list", "-home-u-dev-api"},
			wantErr:     true,
		},
		{
			name:        "the command fails",
			dir:         "/home/u/dev/api",
			fails:       errFailed,
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "list", "-home-u-dev-api"},
			wantErr:     true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex := &fakeExec{out: string(tc.out), err: tc.fails}
			got, err := New(Config{Exec: ex}).List(context.Background(), tc.dir)
			if want := [][]string{tc.wantCommand}; !reflect.DeepEqual(ex.args(), want) {
				t.Errorf("commands %q, want %q", ex.args(), want)
			}
			if (err != nil) != tc.wantErr {
				t.Fatalf("List() error = %v, want error %v", err, tc.wantErr)
			}
			if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("List() =\n %+v\nwant\n %+v", got, tc.want)
			}
		})
	}
}

func TestLookup(t *testing.T) {
	stopped := answer(`[]`,
		"transcript /c/projects/-home-u-dev-api/"+uuid+".jsonl",
		"updated 10",
		`cwd "cwd":"/home/u/dev/api"`,
		`ai-title "aiTitle":"t"`,
	)
	cases := []struct {
		name        string
		id          string
		out         []byte
		fails       error // the command's result
		wantCommand []string
		want        agent.Conversation
		wantErr     error
	}{
		{
			name:        "Stopped: a transcript, not listed by claude agents",
			id:          uuid,
			out:         stopped,
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "locate", uuid},
			want:        agent.Conversation{ID: uuid, Dir: "/home/u/dev/api", State: agent.Stopped, Title: "t", Updated: at(10)},
		},
		{
			name:        "lines a login profile printed first",
			id:          uuid,
			out:         append([]byte(profile), stopped...),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "locate", uuid},
			want:        agent.Conversation{ID: uuid, Dir: "/home/u/dev/api", State: agent.Stopped, Title: "t", Updated: at(10)},
		},
		{
			name:        "asked in upper case",
			id:          upper,
			out:         stopped,
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "locate", uuid},
			want:        agent.Conversation{ID: upper, Dir: "/home/u/dev/api", State: agent.Stopped, Title: "t", Updated: at(10)},
		},
		{
			name: "begun locally and resumed here: the directory named after its project",
			id:   uuid,
			out: answer(`[]`,
				"transcript /c/projects/-home-u-dev-api/"+uuid+".jsonl",
				"updated 10",
				`cwd "cwd":"/Users/me/dev/api"`,
				`cwd "cwd":"/home/u/dev/api"`,
			),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "locate", uuid},
			want:        agent.Conversation{ID: uuid, Dir: "/home/u/dev/api", State: agent.Stopped, Updated: at(10)},
		},
		{
			name: "begun locally, not yet resumed here: the last directory it ran in",
			id:   uuid,
			out: answer(`[]`,
				"transcript /c/projects/-home-u-dev-api/"+uuid+".jsonl",
				"updated 10",
				`cwd "cwd":"/Users/me/dev/api"`,
				`cwd "cwd":"/Users/me/dev/api/lib"`,
			),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "locate", uuid},
			want:        agent.Conversation{ID: uuid, Dir: "/Users/me/dev/api/lib", State: agent.Stopped, Updated: at(10)},
		},
		{
			name:        "Running before it wrote a transcript",
			id:          runningID,
			out:         answer(`[{"sessionId":"` + runningID + `","cwd":"/home/u/dev/api/lib","name":"n","startedAt":3000}]`),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "locate", runningID},
			want:        agent.Conversation{ID: runningID, Dir: "/home/u/dev/api/lib", State: agent.Running, Title: "n", Updated: at(3)},
		},
		{
			name:        "one the program does not have",
			id:          other,
			out:         answer(`[{"sessionId":"` + runningID + `","cwd":"/home/u/dev/api"}]`),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "locate", other},
			wantErr:     agent.ErrUnknown,
		},
		{
			name:        "an ID that is no UUID",
			id:          "../../etc/passwd",
			out:         answer(`[]`),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "locate"},
			wantErr:     agent.ErrUnknown,
		},
		{
			name:        "an unreadable answer",
			id:          uuid,
			out:         []byte("sh: claude: not found\n"),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "locate", uuid},
			wantErr:     errAny,
		},
		{
			name:        "the command fails",
			id:          uuid,
			fails:       errFailed,
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", "locate", uuid},
			wantErr:     errFailed,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ex := &fakeExec{out: string(tc.out), err: tc.fails}
			got, err := New(Config{Exec: ex}).Lookup(context.Background(), tc.id)
			if want := [][]string{tc.wantCommand}; !reflect.DeepEqual(ex.args(), want) {
				t.Errorf("commands %q, want %q", ex.args(), want)
			}
			if !matches(err, tc.wantErr) {
				t.Fatalf("Lookup() error = %v, want %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("Lookup() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
