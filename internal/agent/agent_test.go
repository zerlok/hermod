package agent

import (
	"testing"
)

func TestInvocationString(t *testing.T) {
	cases := []struct {
		invocation Invocation
		want       string
	}{
		{InvokesCommand, "command"},
		{InvokesOneShot, "one-shot"},
		{InvokesConversation, "conversation"},
		{Invocation(9), "Invocation(9)"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.invocation.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStateString(t *testing.T) {
	cases := []struct {
		state State
		want  string
	}{
		{Stopped, "stopped"},
		{Running, "running"},
		{State(7), "unknown state 7"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.state.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewHandshake(t *testing.T) {
	cases := []struct {
		name         string
		version      int
		capabilities string
		wantErr      bool
	}{
		{"an object", 1, `{"loadSession":true}`, false},
		{"an empty object", 1, `{}`, false},
		{"null", 1, `null`, true},
		{"an array", 1, `[]`, true},
		{"not JSON", 1, `{`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, err := NewHandshake(tc.version, []byte(tc.capabilities))
			if (err != nil) != tc.wantErr {
				t.Fatalf("NewHandshake() error = %v, want error %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if h.Version() != tc.version || string(h.Capabilities()) != tc.capabilities {
				t.Fatalf("NewHandshake() = %d, %s, want %d, %s", h.Version(), h.Capabilities(), tc.version, tc.capabilities)
			}
		})
	}
}

func TestZeroHandshakeCapabilities(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"offers the empty object", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(Handshake{}.Capabilities()); got != tc.want {
				t.Fatalf("Capabilities() = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestHandshakeEqual(t *testing.T) {
	cases := []struct {
		name  string
		a, b  string
		va    int
		vb    int
		zeroA bool
		zeroB bool
		equal bool
	}{
		{"the zero handshake and itself", `{}`, `{}`, 0, 0, true, true, true},
		{"the zero handshake and no capabilities", `{}`, `{}`, 0, 0, true, false, true},
		{"the zero handshake and a capability", `{}`, `{"a":true}`, 0, 0, true, false, false},
		{"the same", `{"a":true,"b":{"c":1}}`, `{"a":true,"b":{"c":1}}`, 1, 1, false, false, true},
		{"spaced and ordered otherwise", `{"a":true,"b":{"c":1}}`, `{ "b": {"c": 1}, "a": true }`, 1, 1, false, false, true},
		{"another version", `{"a":true}`, `{"a":true}`, 1, 2, false, false, false},
		{"another capability", `{"a":true}`, `{"a":false}`, 1, 1, false, false, false},
		{"one capability more", `{"a":true}`, `{"a":true,"b":true}`, 1, 1, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, errA := NewHandshake(tc.va, []byte(tc.a))
			b, errB := NewHandshake(tc.vb, []byte(tc.b))
			if errA != nil || errB != nil {
				t.Fatalf("NewHandshake() errors: %v, %v", errA, errB)
			}
			if tc.zeroA {
				a = Handshake{}
			}
			if tc.zeroB {
				b = Handshake{}
			}
			if got := a.Equal(b); got != tc.equal {
				t.Fatalf("Equal() = %v, want %v", got, tc.equal)
			}
		})
	}
}
