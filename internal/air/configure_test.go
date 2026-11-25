package air

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// acpFile is how acp.json is laid out before Configure runs.
type acpFile int

const (
	acpAbsent acpFile = iota
	acpPlain          // a file at ~/.jetbrains/acp.json
	acpLinked         // ~/.jetbrains/acp.json a symlink to a file in dotfiles/
)

const hermodRel = "go/bin/hermod"

// The names a caller gives Configure: hermod's chat command, and the stand-in
// link's name.
var testChatArgs = []string{"air", "acp", "claude"}

const testStandIn = "claude"

func request(root string, mod func(*Request)) Request {
	r := Request{
		Home:     filepath.Join(root, "home"),
		DataHome: filepath.Join(root, "home", ".local", "share"),
		Remote:   "prod-box",
		Hermod:   filepath.Join(root, hermodRel),
		ChatArgs: testChatArgs,

		StandInName:    testStandIn,
		RemoteVariable: "HERMOD_REMOTE",
	}
	if mod != nil {
		mod(&r)
	}
	return r
}

// writeACP lays out acp.json and returns the file that holds its content.
func writeACP(t *testing.T, home string, layout acpFile, content string) string {
	t.Helper()
	path := ACPPath(home)
	switch layout {
	case acpAbsent:
		return path
	case acpPlain:
		mustWrite(t, path, content, 0o644)
		return path
	case acpLinked:
		real := filepath.Join(home, "dotfiles", "acp.json")
		mustWrite(t, real, content, 0o644)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(real, path); err != nil {
			t.Fatal(err)
		}
		return real
	}
	t.Fatalf("unknown layout %d", layout)
	return ""
}

func mustWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// remoteLine is the terminal-mode line request() yields.
const remoteLine = "HERMOD_REMOTE=prod-box"

// TestConfigure asserts the entry is merged into acp.json with every other
// entry and member kept, that a file it cannot read as an object is left
// unchanged, that a dry run changes nothing, and the terminal-mode line it
// returns.
func TestConfigure(t *testing.T) {
	const other = `"Other":{"command":"/usr/bin/other","args":["--x"],"env":{"K":"a&b"}}`
	const entry = `"Claude (prod-box)":{"command":"{hermod}","args":["air","acp","claude","prod-box"]}`
	cases := []struct {
		name     string
		layout   acpFile
		before   string
		req      func(*Request)
		wantErr  bool
		want     string // compact JSON with {hermod} for the binary's path; "" with no error: the file is as before
		wantMode os.FileMode
		wantLink bool   // acp.json is still a symlink
		wantLine string // Result.RemoteLine
	}{
		{
			name: "no file: created private", layout: acpAbsent,
			want: `{"agent_servers":{` + entry + `}}`, wantLine: remoteLine, wantMode: 0o600,
		},
		{
			name: "empty file", layout: acpPlain, before: "\n",
			want: `{"agent_servers":{` + entry + `}}`, wantLine: remoteLine, wantMode: 0o644,
		},
		{
			name: "full file: other entries and members kept", layout: acpPlain,
			before:   `{"default_mcp_settings":{"a":[1,2]},"agent_servers":{` + other + `}}`,
			want:     `{"agent_servers":{"Claude (prod-box)":{"command":"{hermod}","args":["air","acp","claude","prod-box"]},` + other + `},"default_mcp_settings":{"a":[1,2]}}`,
			wantLine: remoteLine, wantMode: 0o644,
		},
		{
			name: "linked file: the link kept, its file updated", layout: acpLinked, before: `{"k":1}`,
			want: `{"agent_servers":{` + entry + `},"k":1}`, wantLine: remoteLine, wantMode: 0o644, wantLink: true,
		},
		{
			name: "same name replaced", layout: acpPlain,
			before:   `{"agent_servers":{"Remote":{"command":"/old","args":[]},` + other + `}}`,
			req:      func(r *Request) { r.Name = "Remote" },
			want:     `{"agent_servers":{` + other + `,"Remote":{"command":"{hermod}","args":["air","acp","claude","prod-box"]}}}`,
			wantLine: remoteLine, wantMode: 0o644,
		},
		{
			name: "agent_servers null", layout: acpPlain, before: `{"agent_servers":null}`,
			want: `{"agent_servers":{` + entry + `}}`, wantLine: remoteLine, wantMode: 0o644,
		},
		{
			name: "name and origin set", layout: acpAbsent,
			req: func(r *Request) {
				r.Name, r.Env = "Remote", map[string]string{"HERMOD_ORIGIN": "work-laptop"}
			},
			want:     `{"agent_servers":{"Remote":{"command":"{hermod}","args":["air","acp","claude","prod-box"],"env":{"HERMOD_ORIGIN":"work-laptop"}}}}`,
			wantLine: remoteLine, wantMode: 0o600,
		},
		{
			name: "origin empty: written empty", layout: acpAbsent,
			req:      func(r *Request) { r.Env = map[string]string{"HERMOD_ORIGIN": ""} },
			want:     `{"agent_servers":{"Claude (prod-box)":{"command":"{hermod}","args":["air","acp","claude","prod-box"],"env":{"HERMOD_ORIGIN":""}}}}`,
			wantLine: remoteLine, wantMode: 0o600,
		},
		{
			name: "origin absent: no env", layout: acpAbsent,
			req:  func(r *Request) { r.Env = map[string]string{} },
			want: `{"agent_servers":{` + entry + `}}`, wantLine: remoteLine, wantMode: 0o600,
		},
		{
			name: "name empty: the default", layout: acpAbsent,
			req:  func(r *Request) { r.Name = "" },
			want: `{"agent_servers":{` + entry + `}}`, wantLine: remoteLine, wantMode: 0o600,
		},
		{
			name: "paths written as they are", layout: acpAbsent,
			req:      func(r *Request) { r.Env = map[string]string{"HERMOD_ORIGIN": "a&b<c>"} },
			want:     `{"agent_servers":{"Claude (prod-box)":{"command":"{hermod}","args":["air","acp","claude","prod-box"],"env":{"HERMOD_ORIGIN":"a&b<c>"}}}}`,
			wantLine: remoteLine, wantMode: 0o600,
		},
		{name: "dry run: nothing written", layout: acpPlain, before: `{"k":1}`, req: func(r *Request) { r.DryRun = true }, wantLine: remoteLine, wantMode: 0o644},
		{
			name: "another remote variable", layout: acpAbsent,
			req:      func(r *Request) { r.RemoteVariable = "AIR_REMOTE" },
			want:     `{"agent_servers":{` + entry + `}}`,
			wantMode: 0o600, wantLine: "AIR_REMOTE=prod-box",
		},
		{name: "no remote variable", layout: acpPlain, before: `{"k":1}`, req: func(r *Request) { r.RemoteVariable = "" }, wantErr: true, wantMode: 0o644},
		{name: "no stand-in name", layout: acpPlain, before: `{"k":1}`, req: func(r *Request) { r.StandInName = "" }, wantErr: true, wantMode: 0o644},
		{name: "no chat arguments", layout: acpPlain, before: `{"k":1}`, req: func(r *Request) { r.ChatArgs = nil }, wantErr: true, wantMode: 0o644},
		{name: "not JSON", layout: acpPlain, before: `{"agent_servers":`, wantErr: true, wantMode: 0o644},
		{name: "not an object", layout: acpPlain, before: `[1]`, wantErr: true, wantMode: 0o644},
		{name: "null", layout: acpPlain, before: `null`, wantErr: true, wantMode: 0o644},
		{name: "agent_servers not an object", layout: acpPlain, before: `{"agent_servers":[]}`, wantErr: true, wantMode: 0o644},
		{name: "no remote", layout: acpPlain, before: `{"k":1}`, req: func(r *Request) { r.Remote = " " }, wantErr: true, wantMode: 0o644},
		{name: "relative hermod", layout: acpPlain, before: `{"k":1}`, req: func(r *Request) { r.Hermod = "hermod" }, wantErr: true, wantMode: 0o644},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			req := request(root, tc.req)
			file := writeACP(t, req.Home, tc.layout, tc.before)

			res, err := Configure(req)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Configure() error = %v, want one: %v", err, tc.wantErr)
			}
			if res.RemoteLine != tc.wantLine {
				t.Errorf("RemoteLine = %q, want %q", res.RemoteLine, tc.wantLine)
			}
			got, readErr := os.ReadFile(file)
			switch {
			case tc.want == "" && tc.layout == acpAbsent:
				if !os.IsNotExist(readErr) {
					t.Errorf("acp.json was written: %q, %v", got, readErr)
				}
				return
			case readErr != nil:
				t.Fatal(readErr)
			case tc.want == "":
				if string(got) != tc.before {
					t.Errorf("acp.json changed to %q", got)
				}
			default:
				want := string(bytes.ReplaceAll([]byte(tc.want), []byte("{hermod}"), []byte(req.Hermod)))
				if compact(t, got) != want {
					t.Errorf("acp.json =\n %s\nwant\n %s", compact(t, got), want)
				}
			}
			if info, err := os.Stat(file); err != nil || info.Mode().Perm() != tc.wantMode {
				t.Errorf("mode = %v, %v, want %o", info, err, tc.wantMode)
			}
			if info, err := os.Lstat(ACPPath(req.Home)); err != nil || (info.Mode()&os.ModeSymlink != 0) != tc.wantLink {
				t.Errorf("acp.json is a link: %v, %v, want %v", info, err, tc.wantLink)
			}
			assertNoLeftovers(t, filepath.Dir(file), filepath.Base(file))
		})
	}
}

// TestConfigureIsIdempotent asserts configuring the same entry again leaves
// the same file, with one entry of that name.
func TestConfigureIsIdempotent(t *testing.T) {
	cases := []struct {
		name      string
		layout    acpFile
		before    string
		req       func(*Request)
		wantNames []string
	}{
		{"default name", acpAbsent, "", nil, []string{"Claude (prod-box)"}},
		{"given name beside another", acpPlain, `{"agent_servers":{"Other":{}}}`, func(r *Request) { r.Name = "Remote" }, []string{"Other", "Remote"}},
		{"through a link", acpLinked, `{}`, nil, []string{"Claude (prod-box)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := request(t.TempDir(), tc.req)
			file := writeACP(t, req.Home, tc.layout, tc.before)
			var files []string
			for range 2 {
				if _, err := Configure(req); err != nil {
					t.Fatal(err)
				}
				b, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				files = append(files, string(b))
			}
			if files[0] != files[1] {
				t.Errorf("the second configure changed the file:\n%s\n%s", files[0], files[1])
			}
			var doc struct {
				Servers map[string]json.RawMessage `json:"agent_servers"`
			}
			if err := json.Unmarshal([]byte(files[1]), &doc); err != nil {
				t.Fatal(err)
			}
			if len(doc.Servers) != len(tc.wantNames) {
				t.Errorf("entries = %d, want %q", len(doc.Servers), tc.wantNames)
			}
			for _, n := range tc.wantNames {
				if _, ok := doc.Servers[n]; !ok {
					t.Errorf("entry %q missing", n)
				}
			}
		})
	}
}

// candidate is what is put at a place the stand-in link may be.
type candidate int

const (
	noClaude      candidate = iota
	linkToHermod            // a symlink to the hermod binary
	linkElsewhere           // a symlink to another program
	plainClaude             // a regular file, as Claude Code's own claude
)

// standInPlace is where Configure is expected to find the link.
type standInPlace int

const (
	foundNone standInPlace = iota
	foundFirstPath
	foundSecondPath
	foundDefault
)

// relativePlace is which place, if any, is named relative to the directory
// configure runs in.
type relativePlace int

const (
	allAbsolute      relativePlace = iota
	firstPathAsDot                 // the first PATH directory given as "."
	dataHomeRelative               // the data directory given relative to the run's directory
)

// TestConfigureFindsTheStandIn asserts the link reported for terminal mode is
// a claude on PATH linked to hermod, else the one at the installer's default
// place, that a place named relative to the run's directory is passed over,
// that the terminal-mode line is returned whether or not a link is found, and
// that no link is created.
func TestConfigureFindsTheStandIn(t *testing.T) {
	cases := []struct {
		name       string
		firstPath  candidate
		secondPath candidate
		atDefault  candidate
		emptyEntry bool // PATH carries an empty element
		relative   relativePlace
		want       standInPlace
		wantLine   string
	}{
		{"none anywhere", noClaude, noClaude, noClaude, false, allAbsolute, foundNone, remoteLine},
		{"on PATH", linkToHermod, noClaude, linkToHermod, false, allAbsolute, foundFirstPath, remoteLine},
		{"Claude Code first on PATH", plainClaude, linkToHermod, noClaude, false, allAbsolute, foundSecondPath, remoteLine},
		{"a link elsewhere passed over", linkElsewhere, noClaude, linkToHermod, false, allAbsolute, foundDefault, remoteLine},
		{"default place only", noClaude, noClaude, linkToHermod, true, allAbsolute, foundDefault, remoteLine},
		{"a plain file at the default place", noClaude, noClaude, plainClaude, false, allAbsolute, foundNone, remoteLine},
		{"a link only through . on PATH", linkToHermod, noClaude, noClaude, false, firstPathAsDot, foundNone, remoteLine},
		{"a link only under a relative data directory", noClaude, noClaude, linkToHermod, false, dataHomeRelative, foundNone, remoteLine},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			first, second := filepath.Join(root, "bin1"), filepath.Join(root, "bin2")
			req := request(root, func(r *Request) {
				r.SearchPath = []string{first, second}
				if tc.emptyEntry {
					r.SearchPath = append([]string{""}, r.SearchPath...)
				}
			})
			mustWrite(t, req.Hermod, "#!/bin/sh\n", 0o755)
			mustWrite(t, filepath.Join(root, "other"), "#!/bin/sh\n", 0o755)
			places := map[standInPlace]string{
				foundNone:       "",
				foundFirstPath:  filepath.Join(first, testStandIn),
				foundSecondPath: filepath.Join(second, testStandIn),
				foundDefault:    StandInPath(req.DataHome, testStandIn),
			}
			put := map[string]candidate{places[foundFirstPath]: tc.firstPath, places[foundSecondPath]: tc.secondPath, places[foundDefault]: tc.atDefault}
			for path, c := range put {
				placeCandidate(t, path, c, req.Hermod, filepath.Join(root, "other"))
			}
			switch tc.relative {
			case firstPathAsDot:
				t.Chdir(first)
				req.SearchPath[0] = "."
			case dataHomeRelative:
				t.Chdir(root)
				rel, err := filepath.Rel(root, req.DataHome)
				if err != nil {
					t.Fatal(err)
				}
				req.DataHome = rel
			}

			res, err := Configure(req)
			if err != nil {
				t.Fatal(err)
			}
			if res.StandIn != places[tc.want] {
				t.Errorf("StandIn = %q, want %q", res.StandIn, places[tc.want])
			}
			if res.RemoteLine != tc.wantLine {
				t.Errorf("RemoteLine = %q, want %q", res.RemoteLine, tc.wantLine)
			}
			for path, c := range put {
				if _, err := os.Lstat(path); (err == nil) != (c != noClaude) {
					t.Errorf("%s: present = %v, want %v", path, err == nil, c != noClaude)
				}
			}
		})
	}
}

func placeCandidate(t *testing.T, path string, c candidate, hermod, other string) {
	t.Helper()
	switch c {
	case noClaude:
		return
	case plainClaude:
		mustWrite(t, path, "#!/bin/sh\n", 0o755)
		return
	}
	target := hermod
	if c == linkElsewhere {
		target = other
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// TestEntry asserts what a dry run prints: the entry under its name.
func TestEntry(t *testing.T) {
	cases := []struct {
		name     string
		entry    string
		remote   string
		hermod   string
		args     []string
		env      map[string]string
		wantName string
		wantJSON string // "" is refused
	}{
		{"default name", "", "prod-box", "/home/me/go/bin/hermod", testChatArgs, nil, "Claude (prod-box)",
			`{"Claude (prod-box)":{"command":"/home/me/go/bin/hermod","args":["air","acp","claude","prod-box"]}}`},
		{"given name with origin", "Remote", "box-b", "/usr/local/bin/hermod", testChatArgs, map[string]string{"HERMOD_ORIGIN": "laptop"}, "Remote",
			`{"Remote":{"command":"/usr/local/bin/hermod","args":["air","acp","claude","box-b"],"env":{"HERMOD_ORIGIN":"laptop"}}}`},
		{"the caller's own arguments", "", "prod-box", "/home/me/go/bin/hermod", []string{"serve"}, nil, "Claude (prod-box)",
			`{"Claude (prod-box)":{"command":"/home/me/go/bin/hermod","args":["serve","prod-box"]}}`},
		{"no arguments", "", "prod-box", "/home/me/go/bin/hermod", nil, nil, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, err := NewEntry(tc.entry, tc.remote, tc.hermod, tc.args, tc.env)
			if (err != nil) != (tc.wantJSON == "") {
				t.Fatalf("NewEntry: %v, want refused %v", err, tc.wantJSON == "")
			}
			if err != nil {
				return
			}
			if e.Name() != tc.wantName {
				t.Errorf("Name() = %q, want %q", e.Name(), tc.wantName)
			}
			got, err := e.JSON()
			if err != nil {
				t.Fatal(err)
			}
			if compact(t, got) != tc.wantJSON {
				t.Errorf("JSON() =\n %s\nwant\n %s", compact(t, got), tc.wantJSON)
			}
		})
	}
}

func assertNoLeftovers(t *testing.T, dir, keep string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != keep {
			t.Errorf("leftover %s in %s", e.Name(), dir)
		}
	}
}

// compact is a JSON document with its whitespace removed, characters kept.
func compact(t *testing.T, doc []byte) string {
	t.Helper()
	var out bytes.Buffer
	inString, escaped := false, false
	for _, c := range string(doc) {
		switch {
		case inString:
			out.WriteRune(c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
		case c == '"':
			inString = true
			out.WriteRune(c)
		case c == ' ' || c == '\n' || c == '\t' || c == '\r':
		default:
			out.WriteRune(c)
		}
	}
	return out.String()
}
