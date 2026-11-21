package mirror

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/zerlok/hermod/internal/shell"
)

// labelled is one session of a `mutagen sync list` listing, with labels.
func labelled(name, kind, sandbox, alpha, beta string) string {
	return separatorLine + "Name: " + name + "\nIdentifier: sync_1\nLabels:\n" +
		"\thermod-kind: " + kind + "\n\thermod-sandbox: " + sandbox + "\n" +
		"Alpha:\n\tURL: " + alpha + "\n\tConnected: Yes\n" +
		"Beta:\n\tURL: " + beta + "\n\tConnected: Yes\n" +
		"Status: Watching for changes\n"
}

func TestList(t *testing.T) {
	const selectorArgv = "mutagen sync list --label-selector hermod-sandbox=prod-box"
	cases := []struct {
		name    string
		probe   reply
		want    []Mirror
		wantErr bool
	}{
		{
			name: "project and transcript mirrors read from their ends",
			probe: reply{ret: shell.Result{Stdout: labelled("hermod-prod-box-dev", "project", "prod-box", "/home/me/dev", "prod-box:dev") +
				labelled("hermod-prod-box-dev-api-transcripts", "transcripts", "prod-box", "prod-box:/home/u/.claude/projects/-home-u-dev-api", "/home/me/.claude/projects/-home-me-dev-api") +
				separatorLine}},
			want: []Mirror{
				{Name: "hermod-prod-box-dev", Kind: Project, Local: "/home/me/dev", Remote: "dev"},
				{Name: "hermod-prod-box-dev-api-transcripts", Kind: Transcripts, Local: "/home/me/.claude/projects/-home-me-dev-api", Remote: "/home/u/.claude/projects/-home-u-dev-api"},
			},
		},
		{
			name: "a session that is not one of the sandbox's mirrors is left out",
			probe: reply{ret: shell.Result{Stdout: labelled("a", "project", "other-box", "/home/me/a", "prod-box:a") +
				labelled("b", "other-kind", "prod-box", "/home/me/b", "prod-box:b") +
				labelled("c", "project", "prod-box", "/home/me/c", "other-box:c") +
				separatorLine + "Name: d\nAlpha:\n\tURL: /home/me/d\nBeta:\n\tURL: prod-box:d\n" + separatorLine}},
		},
		{name: "nothing listed", probe: reply{}},
		{name: "not-found marker lists nothing", probe: notFound},
		{name: "a failed listing fails", probe: reply{err: errors.New("daemon unavailable")}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			probe := &probeShell{replies: map[string]reply{selectorArgv: tc.probe}}
			got, err := List(context.Background(), probe, "prod-box")
			if (err != nil) != tc.wantErr {
				t.Fatalf("List() error = %v, wantErr %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("List() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPlace(t *testing.T) {
	devRoot := Mirror{Name: "hermod-box-dev", Kind: Project, Local: "/home/me/dev", Remote: "dev"}
	apiRoot := Mirror{Name: "hermod-box-dev-api", Kind: Project, Local: "/home/me/dev/api", Remote: "dev/api"}
	optRoot := Mirror{Name: "hermod-box-opt-work", Kind: Project, Local: "/opt/work", Remote: "opt-work"}
	transcripts := Mirror{Name: "hermod-box-dev-api-transcripts", Kind: Transcripts, Local: "/home/me/.claude/projects/-home-me-dev-api", Remote: "/home/u/.claude/projects/-home-u-dev-api"}
	cases := []struct {
		name      string
		mirrors   []Mirror
		localDir  string
		remoteDir string
		want      Placement
	}{
		{
			name:     "no mirrors: a new root at the directory",
			localDir: "/home/me/dev/api", remoteDir: "dev/api",
			want: Placement{Root: Root{"/home/me/dev/api", "dev/api"}, Dir: "dev/api", Roots: []Root{{"/home/me/dev/api", "dev/api"}}},
		},
		{
			name:    "the directory is a mirror's root",
			mirrors: []Mirror{apiRoot}, localDir: "/home/me/dev/api", remoteDir: "dev/api",
			want: Placement{Root: Root{"/home/me/dev/api", "dev/api"}, Dir: "dev/api", Roots: []Root{{"/home/me/dev/api", "dev/api"}}},
		},
		{
			name:    "inside a mirror's root: reused, at the corresponding sandbox path",
			mirrors: []Mirror{optRoot}, localDir: "/opt/work/api/cmd", remoteDir: "opt-work-api-cmd",
			want: Placement{Root: Root{"/opt/work", "opt-work"}, Dir: "opt-work/api/cmd", Roots: []Root{{"/opt/work", "opt-work"}}},
		},
		{
			name:    "a sibling worktree: a new root, the project's root still mapped",
			mirrors: []Mirror{apiRoot, transcripts}, localDir: "/home/me/dev/api-wt", remoteDir: "dev/api-wt",
			want: Placement{Root: Root{"/home/me/dev/api-wt", "dev/api-wt"}, Dir: "dev/api-wt", Roots: []Root{{"/home/me/dev/api-wt", "dev/api-wt"}, {"/home/me/dev/api", "dev/api"}}},
		},
		{
			name:    "nested roots: the innermost wins",
			mirrors: []Mirror{devRoot, apiRoot}, localDir: "/home/me/dev/api/web", remoteDir: "dev/api/web",
			want: Placement{Root: Root{"/home/me/dev/api", "dev/api"}, Dir: "dev/api/web", Roots: []Root{{"/home/me/dev/api", "dev/api"}, {"/home/me/dev", "dev"}}},
		},
		{
			name:    "a parent of mirrored roots: a new root that holds them",
			mirrors: []Mirror{apiRoot, optRoot, transcripts}, localDir: "/home/me", remoteDir: "me",
			want: Placement{Root: Root{"/home/me", "me"}, Dir: "me", Roots: []Root{{"/home/me", "me"}, {"/home/me/dev/api", "dev/api"}, {"/opt/work", "opt-work"}}, Held: []Root{{"/home/me/dev/api", "dev/api"}}},
		},
		{
			name:    "a root's prefix that is not a path segment does not hold the directory",
			mirrors: []Mirror{{Kind: Project, Local: "/home/me/de", Remote: "de"}}, localDir: "/home/me/dev", remoteDir: "dev",
			want: Placement{Root: Root{"/home/me/dev", "dev"}, Dir: "dev", Roots: []Root{{"/home/me/dev", "dev"}, {"/home/me/de", "de"}}},
		},
		{
			name:    "a transcript mirror is no project root",
			mirrors: []Mirror{{Kind: Transcripts, Local: "/home/me", Remote: "/home/u"}}, localDir: "/home/me/dev", remoteDir: "dev",
			want: Placement{Root: Root{"/home/me/dev", "dev"}, Dir: "dev", Roots: []Root{{"/home/me/dev", "dev"}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Place(tc.mirrors, tc.localDir, tc.remoteDir); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Place() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestServing(t *testing.T) {
	devRoot := Mirror{Name: "hermod-box-dev", Kind: Project, Local: "/home/me/dev", Remote: "dev"}
	apiRoot := Mirror{Name: "hermod-box-dev-api", Kind: Project, Local: "/home/me/dev/api", Remote: "dev/api"}
	absRoot := Mirror{Name: "hermod-box--srv-work", Kind: Project, Local: "/home/me/work", Remote: "/srv/work"}
	apiTranscripts := Mirror{Name: "hermod-box-dev-api-transcripts-c0c6a709", Kind: Transcripts, Local: "/home/me/.claude/projects/-home-me-dev-api", Remote: "/home/u/.claude/projects/-home-u-dev-api"}
	webTranscripts := Mirror{Name: "hermod-box-dev-web-transcripts-1d5b1b63", Kind: Transcripts, Local: "/home/me/.claude/projects/-home-me-dev-web", Remote: "/home/u/.claude/projects/-home-u-dev-web"}
	airTranscripts := Mirror{Name: "hermod-box-dev-api-transcripts-611526fd", Kind: Transcripts, Local: "/home/me/.local/share/hermod/air/box/claude/projects/-home-me-dev-api", Remote: "/home/u/.claude/projects/-home-u-dev-api"}
	subTranscripts := Mirror{Name: "hermod-box-dev-api-transcripts-x-1d5b1b63", Kind: Transcripts, Local: "/l", Remote: "/r"}
	project := func(m Mirror) Config {
		return Config{Kind: Project, Host: "box", RemotePath: m.Remote, LocalPath: m.Local}
	}
	transcripts := func(m Mirror, dir string) Config {
		return Config{Kind: Transcripts, Host: "box", RemotePath: m.Remote, LocalPath: m.Local, SessionDir: dir}
	}
	cases := []struct {
		name    string
		mirrors []Mirror
		home    string
		dir     string
		want    []Config
	}{
		{
			name:    "the root holding the directory and its transcripts",
			mirrors: []Mirror{webTranscripts, devRoot, apiTranscripts}, home: "/home/u", dir: "dev/api",
			want: []Config{project(devRoot), transcripts(apiTranscripts, "dev/api")},
		},
		{
			name:    "the directory's transcripts to each local end",
			mirrors: []Mirror{apiTranscripts, airTranscripts}, home: "/home/u", dir: "dev/api",
			want: []Config{transcripts(apiTranscripts, "dev/api"), transcripts(airTranscripts, "dev/api")},
		},
		{
			name:    "a name the directory's prefix starts without a local end's tag is not its",
			mirrors: []Mirror{subTranscripts}, home: "/home/u", dir: "dev/api",
		},
		{
			name:    "nested roots: the innermost",
			mirrors: []Mirror{devRoot, apiRoot}, home: "/home/u", dir: "dev/api/cmd",
			want: []Config{project(apiRoot)},
		},
		{
			name:    "a home-relative root holds an absolute directory under home",
			mirrors: []Mirror{devRoot}, home: "/home/u", dir: "/home/u/dev/api",
			want: []Config{project(devRoot)},
		},
		{
			name:    "an absolute root holds a directory under it",
			mirrors: []Mirror{devRoot, absRoot}, home: "/home/u", dir: "/srv/work/api",
			want: []Config{project(absRoot)},
		},
		{
			name:    "a root's prefix that is not a path segment does not hold it",
			mirrors: []Mirror{{Name: "hermod-box-de", Kind: Project, Local: "/home/me/de", Remote: "de"}}, home: "/home/u", dir: "dev",
		},
		{
			name:    "another directory's transcripts are not the session's",
			mirrors: []Mirror{webTranscripts}, home: "/home/u", dir: "dev/api",
		},
		{name: "nothing listed", home: "/home/u", dir: "dev/api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Serving(tc.mirrors, "box", tc.home, tc.dir); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Serving() = %+v, want %+v", got, tc.want)
			}
		})
	}
}
