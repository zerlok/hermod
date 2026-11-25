package session

import "testing"

func TestMessageID(t *testing.T) {
	cases := []struct {
		name   string
		a, b   MessageID
		equal  bool
		zero   bool   // a is none
		readAs string // a in a log line
	}{
		{"a number and the text of its digits", NumberID(7), TextID("7"), false, false, "7"},
		{"two numbers", NumberID(7), NumberID(7), true, false, "7"},
		{"two texts", TextID("hermod-1"), TextID("hermod-1"), true, false, `"hermod-1"`},
		{"the empty text is an ID", TextID(""), MessageID{}, false, false, `""`},
		{"none", MessageID{}, MessageID{}, true, true, "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if (tc.a == tc.b) != tc.equal || tc.a.IsZero() != tc.zero || tc.a.String() != tc.readAs {
				t.Errorf("%s == %s: %v, IsZero %v, String %q; want %v, %v, %q",
					tc.a, tc.b, tc.a == tc.b, tc.a.IsZero(), tc.a.String(), tc.equal, tc.zero, tc.readAs)
			}
		})
	}
}
