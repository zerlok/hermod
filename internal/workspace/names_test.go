package workspace

import (
	"strings"
	"testing"

	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/worker"
)

func TestNames(t *testing.T) {
	apiPlace := pathmap.Place{Local: "/home/me/dev/api", Remote: "/home/u/dev/api"}
	tags := func(ws, from string) (string, error) {
		ts, err := workerTags(ws, from)
		var words []string
		for _, t := range ts {
			words = append(words, t.Name()+"="+t.Value())
		}
		return strings.Join(words, " "), err
	}
	labels := func(host, ws string) (string, error) {
		var words []string
		for _, l := range fsLabels(host, ws) {
			words = append(words, l.String())
		}
		return strings.Join(words, " "), nil
	}
	keyOf := func(k worker.Key, err error) (string, error) { return k.String(), err }
	cases := []struct {
		name    string
		got     func() (string, error)
		want    string
		wantErr bool
	}{
		{
			name: "an FS relay after its remote and its two ends",
			got:  func() (string, error) { return relayName("prod", apiPlace), nil },
			want: "hermod-prod-home-u-dev-api-48c04bfe",
		},
		{
			name: "an alias reduced to what Mutagen accepts",
			got:  func() (string, error) { return relayName("box.a", apiPlace), nil },
			want: "hermod-box-a-home-u-dev-api-48c04bfe",
		},
		{
			name: "another local end of the same remote root",
			got: func() (string, error) {
				return relayName("prod", pathmap.Place{Local: "/home/me/dev/api-wt", Remote: apiPlace.Remote}), nil
			},
			want: "hermod-prod-home-u-dev-api-2f4b98d0",
		},
		{
			name: "a name cut to the longest label value",
			got: func() (string, error) {
				return relayName("prod", pathmap.Place{
					Local:  apiPlace.Local,
					Remote: "/home/u/projects/a-very-long-organisation-name/another-long-repository-name",
				}), nil
			},
			want: "hermod-prod-home-u-projects-a-very-long-organisation-n-48c04bfe",
		},
		{
			name: "the remote as a label value",
			got:  func() (string, error) { return remoteLabel("-box.a-"), nil },
			want: "box-a",
		},
		{
			name: "an FS relay's labels",
			got:  func() (string, error) { return labels("box.a", "hermod-box-a-home-u-dev-api-48c04bfe") },
			want: "hermod-remote=box-a hermod-workspace=hermod-box-a-home-u-dev-api-48c04bfe",
		},
		{
			name: "an IO relay after its worker",
			got: func() (string, error) {
				k, err := worker.NewKey("hermod-a")
				return ioName("box.a", k), err
			},
			want: "hermod-box-a-io-hermod-a",
		},
		{
			name: "a Net relay after its port",
			got:  func() (string, error) { return netName("prod", 45001), nil },
			want: "hermod-prod-net-45001",
		},
		{
			name: "a conversation's worker",
			got:  func() (string, error) { return keyOf(ConversationKey("0b9e2c1a-5f2d-4c1e-9a7b-3d8e6f4a2b1c")) },
			want: "hermod-0b9e2c1a-5f2d-4c1e-9a7b-3d8e6f4a2b1c",
		},
		{
			name:    "a conversation ID that is no key",
			got:     func() (string, error) { return keyOf(ConversationKey("../x")) },
			wantErr: true,
		},
		{
			name: "an adapter's worker",
			got:  func() (string, error) { return keyOf(adapterKey("hermod-prod-home-u-dev-api-48c04bfe")) },
			want: "hermod-acp-hermod-prod-home-u-dev-api-48c04bfe",
		},
		{
			name: "a worker's tags with an origin",
			got:  func() (string, error) { return tags("hermod-prod-home-u-dev-api-48c04bfe", "My Laptop!") },
			want: "@hermod-workspace=hermod-prod-home-u-dev-api-48c04bfe @hermod-origin=My-Laptop",
		},
		{
			name: "a worker's tags with an origin that reduces to nothing",
			got:  func() (string, error) { return tags("hermod-prod-home-u-dev-api-48c04bfe", " !") },
			want: "@hermod-workspace=hermod-prod-home-u-dev-api-48c04bfe",
		},
		{
			name: "an origin keeps dots",
			got:  func() (string, error) { return origin("box.local"), nil },
			want: "box.local",
		},
		{
			name: "a new root below the home",
			got:  func() (string, error) { return remoteRoot("/home/me", "/home/u", "/home/me/dev/api"), nil },
			want: "/home/u/dev/api",
		},
		{
			name: "a new root at the home itself",
			got:  func() (string, error) { return remoteRoot("/home/me", "/home/u", "/home/me"), nil },
			want: "/home/u/me",
		},
		{
			name: "a new root outside the home",
			got:  func() (string, error) { return remoteRoot("/home/me", "/home/u", "/opt/work/api"), nil },
			want: "/home/u/opt-work-api",
		},
		{
			name: "a new root beside the home",
			got:  func() (string, error) { return remoteRoot("/home/me", "/home/u", "/home/meta"), nil },
			want: "/home/u/home-meta",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.got()
			if (err != nil) != tc.wantErr {
				t.Fatalf("error %v, want error %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
