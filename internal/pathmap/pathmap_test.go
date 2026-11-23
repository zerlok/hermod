package pathmap

import (
	"reflect"
	"strings"
	"testing"
)

func mustMap(t *testing.T, places ...Place) Map {
	t.Helper()
	m, err := New(places...)
	if err != nil {
		t.Fatalf("New(%v) error: %v", places, err)
	}
	return m
}

func TestNewRejectsAmbiguousRoots(t *testing.T) {
	cases := []struct {
		name    string
		places  []Place
		wantErr string // "" for a valid set
	}{
		{"no pairs", nil, ""},
		{"distinct pairs", []Place{{"/home/me/dev", "/home/u/dev"}, {"/home/me/dev/api-wt", "/home/u/dev/api-wt"}}, ""},
		{"empty local root", []Place{{"", "/home/u/dev"}}, "not absolute"},
		{"empty remote root", []Place{{"/home/me/dev", ""}}, "not absolute"},
		{"relative local root", []Place{{"dev", "/home/u/dev"}}, "not absolute"},
		{"relative remote root", []Place{{"/home/me/dev", "dev"}}, "not absolute"},
		{"local root twice", []Place{{"/home/me/dev", "/home/u/a"}, {"/home/me/dev/", "/home/u/b"}}, "local root"},
		{"remote root twice", []Place{{"/home/me/a", "/home/u/dev"}, {"/home/me/b", "/home/u/dev/"}}, "remote root"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.places...)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("New() error: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("New() error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestPath(t *testing.T) {
	nested := []Place{{"/home/me/dev", "/home/u/dev"}, {"/home/me/dev/api-wt", "/home/u/dev/api-wt"}}
	cases := []struct {
		name    string
		places  []Place
		toLocal bool
		in      string
		want    string
	}{
		{"longest prefix wins", nested, false, "/home/me/dev/api-wt/main.go", "/home/u/dev/api-wt/main.go"},
		{"shorter root for a sibling", nested, false, "/home/me/dev/api/main.go", "/home/u/dev/api/main.go"},
		{"longest prefix wins to local", nested, true, "/home/u/dev/api-wt/main.go", "/home/me/dev/api-wt/main.go"},
		{"segment boundary respected", []Place{{"/home/me/dev/api", "/home/u/dev/api"}}, false, "/home/me/dev/api2/x", "/home/me/dev/api2/x"},
		{"root itself", []Place{{"/home/me/dev/api", "/home/u/dev/api"}}, false, "/home/me/dev/api", "/home/u/dev/api"},
		{"remote to local", []Place{{"/home/me/dev/api", "/home/u/dev/api"}}, true, "/home/u/dev/api/x.go", "/home/me/dev/api/x.go"},
		{"under no root unchanged", []Place{{"/home/me/dev/api", "/home/u/dev/api"}}, false, "/etc/hosts", "/etc/hosts"},
		{"other side's root not applied", []Place{{"/home/me/dev/api", "/home/u/dev/api"}}, false, "/home/u/dev/api/x", "/home/u/dev/api/x"},
		{"trailing slash on root ignored", []Place{{"/home/me/dev/api/", "/home/u/dev/api/"}}, false, "/home/me/dev/api/x", "/home/u/dev/api/x"},
		{"rest kept as written", []Place{{"/home/me/dev/api", "/home/u/dev/api"}}, false, "/home/me/dev/api//x/../y", "/home/u/dev/api//x/../y"},
		{"filesystem root maps below", []Place{{"/", "/mnt/local"}}, false, "/etc/hosts", "/mnt/local/etc/hosts"},
		{"filesystem root itself", []Place{{"/", "/mnt/local"}}, false, "/", "/mnt/local"},
		{"to filesystem root", []Place{{"/", "/mnt/local"}}, true, "/mnt/local/etc", "/etc"},
		{"relative path not under filesystem root", []Place{{"/", "/mnt/local"}}, false, "etc", "etc"},
		{"relative path not under a root's tail", []Place{{"/home/me/dev", "/home/u/dev"}}, true, "dev/feature", "dev/feature"},
		{"empty string unchanged", []Place{{"/home/me/dev", "/home/u/dev"}}, false, "", ""},
		{"no roots", nil, false, "/home/me/dev/x", "/home/me/dev/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := mustMap(t, tc.places...)
			mapPath := m.ToRemote
			if tc.toLocal {
				mapPath = m.ToLocal
			}
			if got := mapPath(tc.in); got != tc.want {
				t.Errorf("map(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestZeroMapMapsNothing(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"absolute", "/home/me/x"},
		{"relative", "dev/x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, mapPath := range []func(string) string{Map{}.ToRemote, Map{}.ToLocal} {
				if got := mapPath(tc.in); got != tc.in {
					t.Errorf("map(%q) = %q, want it unchanged", tc.in, got)
				}
			}
		})
	}
}

func TestJSON(t *testing.T) {
	api := []Place{{"/home/me/dev/api", "/home/u/dev/api"}}
	cases := []struct {
		name    string
		places  []Place
		toLocal bool
		in      string
		want    string
		wantErr bool
	}{
		{
			"nested strings mapped, other fields unchanged",
			api, true,
			`{"cwd":"/home/u/dev/api","tool_input":{"file_path":"/home/u/dev/api/x.go","limit":10},"ok":true,"none":null}`,
			`{"cwd":"/home/me/dev/api","tool_input":{"file_path":"/home/me/dev/api/x.go","limit":10},"ok":true,"none":null}`,
			false,
		},
		{
			"keys are never mapped",
			api, true,
			`{"/home/u/dev/api" : "/home/u/dev/api/a"}`,
			`{"/home/u/dev/api" : "/home/me/dev/api/a"}`,
			false,
		},
		{
			"arrays at any depth",
			api, true,
			`[["/home/u/dev/api/a"],{"x":["/home/u/dev/api/b",1.50]}]`,
			`[["/home/me/dev/api/a"],{"x":["/home/me/dev/api/b",1.50]}]`,
			false,
		},
		{
			"member order and whitespace kept",
			api, false,
			"{ \"z\": \"/home/me/dev/api\",\n  \"a\": 1e3 }",
			"{ \"z\": \"/home/u/dev/api\",\n  \"a\": 1e3 }",
			false,
		},
		{
			"unmapped string keeps its escapes",
			api, true,
			`{"s":"/etc\/hosts \"q\""}`,
			`{"s":"/etc\/hosts \"q\""}`,
			false,
		},
		{
			"escaped path is decoded before mapping",
			api, true,
			`{"p":"\/home\/u\/dev\/api\/x"}`,
			`{"p":"/home/me/dev/api/x"}`,
			false,
		},
		{
			"mapped string written without HTML escapes",
			[]Place{{"/home/me/a&b", "/home/u/a<b>"}}, false,
			`"/home/me/a&b/x"`,
			`"/home/u/a<b>/x"`,
			false,
		},
		{
			"escaped quote before a colon inside a value",
			api, true,
			`{"k":"a\": b","p":"/home/u/dev/api/c"}`,
			`{"k":"a\": b","p":"/home/me/dev/api/c"}`,
			false,
		},
		{"bare string document", api, true, `"/home/u/dev/api/x"`, `"/home/me/dev/api/x"`, false},
		{"invalid JSON", api, true, `{"cwd":`, "", true},
		{"empty document", api, true, ``, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := mustMap(t, tc.places...)
			mapJSON := m.ToRemoteJSON
			if tc.toLocal {
				mapJSON = m.ToLocalJSON
			}
			got, err := mapJSON([]byte(tc.in))
			if (err != nil) != tc.wantErr {
				t.Fatalf("JSON() error = %v, wantErr %v", err, tc.wantErr)
			}
			if string(got) != tc.want {
				t.Errorf("JSON() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestPlaces(t *testing.T) {
	cases := []struct {
		name   string
		places []Place
		want   []Place
	}{
		{"none", nil, nil},
		{
			"longest local root first, trailing slashes dropped",
			[]Place{{"/home/me/dev", "/home/u/dev"}, {"/home/me/dev/api-wt/", "/home/u/wt"}, {"/", "/mnt/local"}},
			[]Place{{"/home/me/dev/api-wt", "/home/u/wt"}, {"/home/me/dev", "/home/u/dev"}, {"/", "/mnt/local"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mustMap(t, tc.places...).Places(); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Places() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUnder(t *testing.T) {
	cases := []struct {
		name    string
		p, root string
		wantRel string
		wantOK  bool
	}{
		{"the root itself", "/home/u/dev", "/home/u/dev", ".", true},
		{"below the root", "/home/u/dev/api/cmd", "/home/u/dev", "api/cmd", true},
		{"a prefix that is no segment", "/home/u/devops", "/home/u/dev", "", false},
		{"beside the root", "/home/u/web", "/home/u/dev", "", false},
		{"under /", "/srv/x", "/", "srv/x", true},
		{"/ itself", "/", "/", ".", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rel, ok := Under(tc.p, tc.root)
			if rel != tc.wantRel || ok != tc.wantOK {
				t.Errorf("Under(%q, %q) = %q, %v, want %q, %v", tc.p, tc.root, rel, ok, tc.wantRel, tc.wantOK)
			}
		})
	}
}

func TestAbsolute(t *testing.T) {
	cases := []struct {
		name    string
		home, p string
		want    string
	}{
		{"home-relative", "/home/u", "dev/api", "/home/u/dev/api"},
		{"absolute kept", "/home/u", "/srv/work", "/srv/work"},
		{"absolute cleaned", "/home/u", "/srv/work/", "/srv/work"},
		{"relative cleaned", "/home/u", "dev/./api/", "/home/u/dev/api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Absolute(tc.home, tc.p); got != tc.want {
				t.Errorf("Absolute(%q, %q) = %q, want %q", tc.home, tc.p, got, tc.want)
			}
		})
	}
}
