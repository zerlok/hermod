package shell

import "strings"

// join renders an argv as a single space-separated line with each element
// shell-quoted. It collapses a command into one argument when crossing a host
// boundary: ssh joins its trailing args with spaces, so the command must arrive
// pre-quoted as a single string to survive the remote shell re-parsing it.
func join(argv []string) string {
	parts := make([]string, len(argv))
	for i, a := range argv {
		parts[i] = quote(a)
	}
	return strings.Join(parts, " ")
}

// quote returns s verbatim when it is made only of shell-safe characters,
// otherwise single-quoted with embedded single quotes escaped.
func quote(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		if !isShellSafe(r) {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}

func isShellSafe(r rune) bool {
	switch {
	case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		return true
	default:
		return strings.ContainsRune("_-./:=@,", r)
	}
}
