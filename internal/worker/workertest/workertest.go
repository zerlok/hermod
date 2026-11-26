// Package workertest reads back the commands package worker sends a remote,
// for a fake remote in tests: which script runs, with what, on which tmux
// session, and what a start makes. A fake keeps its own state and answers from
// it; how worker's scripts and shell's tmux commands lay their arguments out
// is read here alone.
package workertest

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Command is one command worker sends the remote.
type Command struct {
	Script  string   // "write", "stream", "keep" or "act"
	Op      string   // act's operation
	Args    []string // act's arguments after its operation; the other scripts' arguments
	Refused int      // the status act and keep exit with for a refused operation
	Lingers int      // act's status for a closed process that outlived the wait
	Failed  int      // act's status for a tmux that could not tell which sessions exist
	Session string   // the session an act operation's tmux command names, or keep makes
	Format  string   // the format a list's tmux command prints each session in
	Listed  []string // the options a list asks for
	Look    Look     // what a look reads
	Ports   []string // the candidates a port operation picks from
	Start   Start    // what keep starts
}

// Look is what a look reads beside the worker's state.
type Look struct {
	WorkerDir string   // under the home
	Line      string   // the command line it runs; "" for none
	Files     []string // the files of the worker's directory it reads
}

// Start is what keep starts: the session's tags and its process.
type Start struct {
	WorkerDir string            // under the home
	Staging   string            // under the home
	Tags      map[string]string // by option name
	Dir       string            // where the process starts
	Form      string            // "terminal" or "stream"
	Command   []string          // the process; none for the login shell
}

// Read is the command words run on the remote; ok is false for a command that
// is none of worker's.
func Read(words []string) (c Command, ok bool) {
	switch {
	case len(words) > 3 && words[0] == "sh" && words[1] == "-s" && words[2] == "--":
		args := words[3:]
		if len(args) > 4 && args[4] == "tmux" {
			return readKeep(args)
		}
		return Command{Script: "stream", Args: args}, true
	case len(words) > 3 && words[0] == "sh" && words[1] == "-c" && words[3] == "write":
		return Command{Script: "write", Args: words[4:]}, true
	case len(words) > 8 && words[0] == "sh" && words[1] == "-c" && words[3] == "act":
		return readAct(words[4:])
	}
	return Command{}, false
}

// readAct reads act.sh's arguments: REFUSED LINGERS UNREADABLE ABSENT OP ARG...
func readAct(args []string) (Command, bool) {
	c := Command{Script: "act", Op: args[4], Args: args[5:]}
	var err error
	if c.Refused, err = strconv.Atoi(args[0]); err != nil {
		return Command{}, false
	}
	if c.Lingers, err = strconv.Atoi(args[1]); err != nil {
		return Command{}, false
	}
	if c.Failed, err = strconv.Atoi(args[2]); err != nil {
		return Command{}, false
	}
	switch c.Op {
	case "list":
		words := Words(c.Args[0])
		c.Format = words[slices.Index(words, "-F")+1]
		for _, m := range field.FindAllStringSubmatch(c.Format, -1) {
			if strings.HasPrefix(m[1], "@") {
				c.Listed = append(c.Listed, m[1])
			}
		}
	case "close", "remove", "attach", "log":
		c.Session = target(c.Args[0])
	case "look":
		c.Session = target(c.Args[0])
		c.Look = Look{WorkerDir: c.Args[1], Line: c.Args[2], Files: c.Args[3:]}
	case "port":
		c.Ports = c.Args[3:]
	}
	return c, true
}

// target is the session a tmux command line names with `-t =name:`.
func target(line string) string {
	words := Words(line)
	t := words[slices.Index(words, "-t")+1]
	return strings.TrimSuffix(strings.TrimPrefix(t, "="), ":")
}

// readKeep reads keep.sh's arguments, WDIR STAGING REFUSED HAS, then the tmux
// command that makes the session: the session's command after `--`, then a
// `set-option -t T -qo NAME VALUE` after each lone ";". The command is
// pane.sh under the login shell, whose line is its last word.
func readKeep(args []string) (Command, bool) {
	refused, err := strconv.Atoi(args[2])
	if err != nil {
		return Command{}, false
	}
	tmux := args[4:]
	c := Command{Script: "keep", Args: args, Refused: refused, Session: tmux[slices.Index(tmux, "-s")+1]}
	c.Start = Start{WorkerDir: args[0], Staging: args[1], Tags: map[string]string{}}
	parts := split(tmux, ";")
	for _, p := range parts[1:] {
		if i := slices.Index(p, "-qo"); i >= 0 && i+2 < len(p) {
			c.Start.Tags[p[i+1]] = tmuxWord(p[i+2])
		}
	}
	session := parts[0]
	login := session[slices.Index(session, "--")+1:]
	pane := Words(tmuxWord(login[len(login)-1]))
	// sh -c SCRIPT pane WDIR STAGING DIR MARK PID ENV FORM IN OUT ERR KEEP COMMAND...
	if len(pane) < 15 {
		return Command{}, false
	}
	c.Start.Dir, c.Start.Form, c.Start.Command = pane[6], pane[10], pane[15:]
	return c, true
}

// split cuts words at each word sep.
func split(words []string, sep string) [][]string {
	parts := [][]string{nil}
	for _, w := range words {
		if w == sep {
			parts = append(parts, nil)
			continue
		}
		parts[len(parts)-1] = append(parts[len(parts)-1], w)
	}
	return parts
}

// tmuxWord is a word of tmux's command line as tmux reads it: `\;` at its
// end is a literal ';'.
func tmuxWord(w string) string {
	if s, ok := strings.CutSuffix(w, `\;`); ok {
		return s + ";"
	}
	return w
}

// Listed is one session a fake remote holds, as a listing prints it.
type Listed struct {
	Name string
	Dead bool
	Tags map[string]string // by option name
}

var field = regexp.MustCompile(`#\{([^}]*)\}`)

// List is what act's list prints for the sessions, each in the list command's
// format, then the line that ends a complete listing.
func (c Command) List(sessions []Listed) string {
	var b strings.Builder
	for _, s := range sessions {
		b.WriteString(field.ReplaceAllStringFunc(c.Format, func(f string) string {
			switch name := field.FindStringSubmatch(f)[1]; name {
			case "pane_dead":
				if s.Dead {
					return "1"
				}
				return "0"
			case "session_name":
				return s.Name
			default:
				return s.Tags[name]
			}
		}) + "\n")
	}
	b.WriteString("end\n")
	return b.String()
}

// Words splits a line the way a POSIX shell splits the words shell quotes:
// bare words, single-quoted parts and backslash escapes.
func Words(line string) []string {
	var (
		words  []string
		cur    strings.Builder
		inWord bool
		quoted bool
	)
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quoted && c == '\'':
			quoted = false
		case quoted:
			cur.WriteByte(c)
		case c == '\'':
			quoted, inWord = true, true
		case c == '\\' && i+1 < len(line):
			i++
			cur.WriteByte(line[i])
			inWord = true
		case c == ' ':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words
}
