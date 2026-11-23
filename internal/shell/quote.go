package shell

import "strings"

// join renders arguments as one space-separated line, each shell-quoted. It
// collapses a command into one argument when crossing to another workstation:
// ssh joins its trailing arguments with spaces, so the command must arrive
// quoted as a single string to survive the remote shell parsing it again.
func join(args []string) string {
	parts := make([]string, len(args))
	for i, a := range args {
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

// Quote is s as one word of a POSIX shell line, for a caller that writes a
// line itself, such as a file the remote's shell sources.
func Quote(s string) string { return quote(s) }
