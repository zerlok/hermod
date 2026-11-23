package claude

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zerlok/hermod/internal/agent"
)

const (
	other     = "7a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
	runningID = "1f2e3d4c-5b6a-4978-8695-a4b3c2d1e0f9"

	// remoteConfig is Claude's config directory on the remote the tests' agent
	// is given.
	remoteConfig = "/c"
)

// answer is the script's report: its lines, then the conversations
// Claude runs.
func answer(agentsJSON string, lines ...string) []byte {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	b.WriteString("agents:\n" + agentsJSON + "\n")
	return []byte(b.String())
}

func at(seconds int64) time.Time { return time.Unix(seconds, 0).UTC() }

func TestList(t *testing.T) {
	cases := []struct {
		name        string
		dir         string
		out         []byte
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
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "list", "-home-u-dev-api"},
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
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "list", "-home-u-dev-api"},
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
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "list", "-home-u-dev-api"},
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
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "list", "-home-u-dev-api"},
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
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "list", "-home-u-dev-api"},
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
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "list", "-home-u-dev-api"},
			want:        []agent.Conversation{{ID: uuid, Dir: "/home/u/dev/api", State: agent.Stopped, Updated: at(10)}},
		},
		{
			name:        "none",
			dir:         "/",
			out:         answer(`[]`),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "list", "-"},
		},
		{
			name:        "no list of runningID conversations",
			dir:         "/home/u/dev/api",
			out:         []byte("transcript /c/projects/x/" + uuid + ".jsonl\n"),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "list", "-home-u-dev-api"},
			wantErr:     true,
		},
		{
			name:        "claude agents failed",
			dir:         "/home/u/dev/api",
			out:         answer("error: unknown command 'agents'"),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "list", "-home-u-dev-api"},
			wantErr:     true,
		},
		{
			name:        "a line the script does not write",
			dir:         "/home/u/dev/api",
			out:         answer(`[]`, "transcript /c/projects/x/"+uuid+".jsonl", "size 12"),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "list", "-home-u-dev-api"},
			wantErr:     true,
		},
		{
			name:        "a line before any transcript",
			dir:         "/home/u/dev/api",
			out:         answer(`[]`, `cwd "cwd":"/home/u/dev/api"`),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "list", "-home-u-dev-api"},
			wantErr:     true,
		},
		{
			name:        "a malformed member",
			dir:         "/home/u/dev/api",
			out:         answer(`[]`, "transcript /c/projects/x/"+uuid+".jsonl", `cwd "cwd":"/home/u`),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "list", "-home-u-dev-api"},
			wantErr:     true,
		},
		{
			name:        "an unreadable last change",
			dir:         "/home/u/dev/api",
			out:         answer(`[]`, "transcript /c/projects/x/"+uuid+".jsonl", "updated yesterday"),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "list", "-home-u-dev-api"},
			wantErr:     true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			listing := New(Config{RemoteConfigDir: remoteConfig}).List(tc.dir)
			if got := listing.Command(); !reflect.DeepEqual(got, tc.wantCommand) {
				t.Errorf("Command() = %q, want %q", got, tc.wantCommand)
			}
			got, err := listing.Read(tc.out)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Read() error = %v, want error %v", err, tc.wantErr)
			}
			if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Read() =\n %+v\nwant\n %+v", got, tc.want)
			}
		})
	}
}

func TestLocate(t *testing.T) {
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
		wantCommand []string
		want        agent.Conversation
		wantFound   bool
		wantErr     bool
	}{
		{
			name:        "Stopped: a transcript, not listed by claude agents",
			id:          uuid,
			out:         stopped,
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "locate", uuid},
			want:        agent.Conversation{ID: uuid, Dir: "/home/u/dev/api", State: agent.Stopped, Title: "t", Updated: at(10)},
			wantFound:   true,
		},
		{
			name:        "asked in upper case",
			id:          upper,
			out:         stopped,
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "locate", uuid},
			want:        agent.Conversation{ID: upper, Dir: "/home/u/dev/api", State: agent.Stopped, Title: "t", Updated: at(10)},
			wantFound:   true,
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
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "locate", uuid},
			want:        agent.Conversation{ID: uuid, Dir: "/home/u/dev/api", State: agent.Stopped, Updated: at(10)},
			wantFound:   true,
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
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "locate", uuid},
			want:        agent.Conversation{ID: uuid, Dir: "/Users/me/dev/api/lib", State: agent.Stopped, Updated: at(10)},
			wantFound:   true,
		},
		{
			name:        "Running before it wrote a transcript",
			id:          runningID,
			out:         answer(`[{"sessionId":"` + runningID + `","cwd":"/home/u/dev/api/lib","name":"n","startedAt":3000}]`),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "locate", runningID},
			want:        agent.Conversation{ID: runningID, Dir: "/home/u/dev/api/lib", State: agent.Running, Title: "n", Updated: at(3)},
			wantFound:   true,
		},
		{
			name:        "one the program does not have",
			id:          other,
			out:         answer(`[{"sessionId":"` + runningID + `","cwd":"/home/u/dev/api"}]`),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "locate", other},
		},
		{
			name:        "an ID that is no UUID",
			id:          "../../etc/passwd",
			out:         answer(`[]`),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "locate"},
		},
		{
			name:        "an unreadable answer",
			id:          uuid,
			out:         []byte("sh: claude: not found\n"),
			wantCommand: []string{"sh", "-c", conversationsScript, "conversations", remoteConfig, "locate", uuid},
			wantErr:     true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lookup := New(Config{RemoteConfigDir: remoteConfig}).Locate(tc.id)
			if got := lookup.Command(); !reflect.DeepEqual(got, tc.wantCommand) {
				t.Errorf("Command() = %q, want %q", got, tc.wantCommand)
			}
			got, found, err := lookup.Read(tc.out)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Read() error = %v, want error %v", err, tc.wantErr)
			}
			if got != tc.want || found != tc.wantFound {
				t.Errorf("Read() = %+v, %v, want %+v, %v", got, found, tc.want, tc.wantFound)
			}
		})
	}
}
