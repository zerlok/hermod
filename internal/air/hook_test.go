package air

import (
	"net/url"
	"testing"
)

func TestIsHook(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"http://127.0.0.1:63342/air/hooks/claude/stop", true},
		{"http://localhost:1/air/", true},
		{"http://127.0.0.1:63342/other/hook", false},
		{"http://127.0.0.1:63342/airship", false},
		{"http://127.0.0.1:63342/", false},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			u, err := url.Parse(tc.url)
			if err != nil {
				t.Fatal(err)
			}
			if got := IsHook(u); got != tc.want {
				t.Errorf("IsHook(%s) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}
