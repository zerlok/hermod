package control

import (
	"reflect"
	"testing"
)

func TestRemoteDir(t *testing.T) {
	cases := []struct {
		name     string
		localDir string
		home     string
		want     string
	}{
		{"under home nested", "/home/u/dev/api", "/home/u", "dev/api"},
		{"directly under home", "/home/u/api", "/home/u", "api"},
		{"outside home slugified", "/opt/work/api", "/home/u", "opt-work-api"},
		{"sibling of home slugified", "/home/other/api", "/home/u", "home-other-api"},
		{"home unknown falls back to slug", "/home/u/dev/api", "", "home-u-dev-api"},
		{"trailing slash normalized", "/home/u/dev/api/", "/home/u", "dev/api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := remoteDir(tc.localDir, tc.home); got != tc.want {
				t.Errorf("remoteDir(%q, %q) = %q, want %q", tc.localDir, tc.home, got, tc.want)
			}
		})
	}
}

// TestWithWorkdir covers the option that carries the path-resolution logic: it
// resolves the directory and derives both the remote path and the default
// session name.
func TestWithWorkdir(t *testing.T) {
	cases := []struct {
		name           string
		dir, cwd, home string
		wantLocal      string
		wantRemote     string
		wantSession    string
	}{
		{"default dir from cwd", "", "/home/u/api", "/home/u", "/home/u/api", "api", "api"},
		{"nested path slug session", "", "/home/u/dev/repos/myproj", "/home/u", "/home/u/dev/repos/myproj", "dev/repos/myproj", "dev-repos-myproj"},
		{"outside home full slug", "", "/opt/work/api", "/home/u", "/opt/work/api", "opt-work-api", "opt-work-api"},
		{"tilde expanded", "~/work/api", "/tmp", "/home/u", "/home/u/work/api", "work/api", "work-api"},
		{"relative joined to cwd", "sub/api", "/home/u/work", "/home/u", "/home/u/work/sub/api", "work/sub/api", "work-sub-api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var o Options
			WithWorkdir(tc.dir, tc.cwd, tc.home)(&o)
			got := Options{LocalDir: o.LocalDir, RemoteDir: o.RemoteDir, Session: o.Session}
			want := Options{LocalDir: tc.wantLocal, RemoteDir: tc.wantRemote, Session: tc.wantSession}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("after WithWorkdir = %+v, want %+v", got, want)
			}
		})
	}
}

// TestWithSession asserts an explicit session wins over the derived default and
// that an empty name leaves the default, regardless of option order.
func TestWithSession(t *testing.T) {
	cases := []struct {
		name  string
		apply func(*Options)
		want  string
	}{
		{"explicit before workdir", func(o *Options) {
			WithSession("chosen")(o)
			WithWorkdir("", "/home/u/api", "/home/u")(o)
		}, "chosen"},
		{"explicit after workdir", func(o *Options) {
			WithWorkdir("", "/home/u/api", "/home/u")(o)
			WithSession("chosen")(o)
		}, "chosen"},
		{"empty keeps derived default", func(o *Options) {
			WithWorkdir("", "/home/u/api", "/home/u")(o)
			WithSession("")(o)
		}, "api"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var o Options
			tc.apply(&o)
			if o.Session != tc.want {
				t.Errorf("Session = %q, want %q", o.Session, tc.want)
			}
		})
	}
}
