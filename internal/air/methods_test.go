package air

import (
	"testing"

	"github.com/zerlok/hermod/internal/session"
)

func TestMethods(t *testing.T) {
	cases := []struct {
		method string
		want   session.Kind
		listed bool // the table names it; a standard method is acp's
	}{
		{"session/close", session.Suspend, true},
		{"_session/archive", session.Close, true},
		{"_session/unarchive", session.Pass, false},
		{"initialize", session.Pass, false},
		{"session/list", session.Pass, false},
		{"session/new", session.Pass, false},
		{"$/cancel_request", session.Pass, false},
	}
	methods := Methods()
	listed := 0
	for _, tc := range cases {
		t.Run(tc.method, func(t *testing.T) {
			got, ok := methods[tc.method]
			if got != tc.want || ok != tc.listed {
				t.Errorf("Methods()[%q] = %s, %v, want %s, %v", tc.method, got, ok, tc.want, tc.listed)
			}
		})
		if tc.listed {
			listed++
		}
	}
	if len(methods) != listed {
		t.Errorf("Methods() has %d entries, want %d", len(methods), listed)
	}
}
