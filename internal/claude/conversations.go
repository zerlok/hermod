package claude

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zerlok/hermod/internal/agent"
	"github.com/zerlok/hermod/internal/pathmap"
)

// conversationsScript reports Claude's transcripts and the conversations it
// runs. It is sent to the remote's sh as it is, its inputs as arguments.
//
//go:embed conversations.sh
var conversationsScript string

// conversations is the command that runs conversationsScript with args, in
// the remote config directory the transcripts' place uses, so the conversations
// reported are those the relay keeps.
func (a Agent) conversations(args ...string) []string {
	return append([]string{"sh", "-c", conversationsScript, "conversations", a.cfg.RemoteConfigDir}, args...)
}

// List asks Claude on the remote for the conversations of dir and of the
// directories under it: each one Claude keeps a transcript of, in the
// directory the transcript names, and each one it runs, Running.
func (a Agent) List(dir string) agent.Listing {
	dir = path.Clean(dir)
	return agent.NewListing(dir, a.conversations("list", projectDirName(dir)), func(out []byte) ([]agent.Conversation, error) {
		all, err := readConversations(out, dir)
		if err != nil {
			return nil, err
		}
		// The transcripts' directory names lose which character was a '/', so
		// the script reports a superset, and the transcripts' own directories
		// decide.
		return slices.DeleteFunc(all, func(c agent.Conversation) bool {
			_, under := pathmap.Under(c.Dir, dir)
			return !under
		}), nil
	})
}

// Locate asks Claude on the remote for one conversation: where its
// transcript says it ran, or where Claude runs it. An ID that is no UUID is
// none Claude has.
func (a Agent) Locate(id string) agent.Lookup {
	args := []string{"locate"}
	if isSessionID(id) {
		args = append(args, strings.ToLower(id))
	}
	return agent.NewLookup(id, a.conversations(args...), func(out []byte) (agent.Conversation, bool, error) {
		all, err := readConversations(out, "")
		if err != nil {
			return agent.Conversation{}, false, err
		}
		for _, c := range all {
			if strings.EqualFold(c.ID, id) {
				c.ID = id
				return c, true, nil
			}
		}
		return agent.Conversation{}, false, nil
	})
}

// transcript is what the script reported of one transcript.
type transcript struct {
	id          string
	project     string // the name of the directory under projects/ it is in
	updated     time.Time
	cwds        []string
	customTitle string
	aiTitle     string
}

// dir is the directory on this workstation the transcript belongs to; none
// when it names no directory at all. Claude keeps a transcript under the
// project named after the directory it runs in, while a transcript begun on
// another workstation still names that one's directory in its first messages,
// so the directory named after its project wins. A transcript carried over and
// not yet resumed here names no directory of this workstation, so the one
// asked for, when its project is the transcript's, comes next, and the last
// directory the transcript ran in last.
func (t transcript) dir(asked string) string {
	if len(t.cwds) == 0 {
		return ""
	}
	for _, c := range t.cwds {
		if projectDirName(c) == t.project {
			return c
		}
	}
	if asked != "" && projectDirName(asked) == t.project {
		return asked
	}
	return t.cwds[len(t.cwds)-1]
}

// title is the user's own title, else Claude's.
func (t transcript) title() string {
	if t.customTitle != "" {
		return t.customTitle
	}
	return t.aiTitle
}

// readConversations reads the script's report into conversations, newest
// first; asked is the directory listed, if any. A conversation is Running when
// `claude agents` lists it and Stopped otherwise. A transcript whose file is not named by a UUID is not a
// conversation's, and a conversation with no absolute, clean directory cannot
// be placed, so both are left out rather than failing the rest.
func readConversations(out []byte, asked string) ([]agent.Conversation, error) {
	report, agentsJSON, found := cutLine(out, "agents:")
	if !found {
		return nil, fmt.Errorf("claude: conversations: no list of running conversations in the answer")
	}
	transcripts, err := readTranscripts(report)
	if err != nil {
		return nil, err
	}
	running, err := readAgents(agentsJSON)
	if err != nil {
		return nil, err
	}
	byID := map[string]agent.Conversation{}
	for _, t := range transcripts {
		key := strings.ToLower(t.id)
		if seen, ok := byID[key]; ok && seen.Updated.After(t.updated) {
			continue
		}
		byID[key] = agent.Conversation{ID: t.id, Dir: t.dir(asked), State: agent.Stopped, Title: t.title(), Updated: t.updated}
	}
	for key, r := range running {
		c, ok := byID[key]
		if !ok {
			c = agent.Conversation{ID: r.SessionID, Updated: fromMillis(r.StartedAt)}
		}
		c.State = agent.Running
		if c.Dir == "" {
			c.Dir = r.Cwd
		}
		if c.Title == "" {
			c.Title = r.Name
		}
		byID[key] = c
	}
	var all []agent.Conversation
	for _, c := range byID {
		if path.IsAbs(c.Dir) && path.Clean(c.Dir) == c.Dir {
			all = append(all, c)
		}
	}
	slices.SortFunc(all, func(a, b agent.Conversation) int {
		if c := b.Updated.Compare(a.Updated); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return all, nil
}

// cutLine splits out around its first line that reads exactly line.
func cutLine(out []byte, line string) (before, after []byte, found bool) {
	if rest, ok := bytes.CutPrefix(out, []byte(line+"\n")); ok {
		return nil, rest, true
	}
	return bytes.Cut(out, []byte("\n"+line+"\n"))
}

func fromMillis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

// readTranscripts reads the transcript lines of the script's report. A line
// the script does not write means the answer is another program's, so it fails
// the reading.
func readTranscripts(report []byte) ([]transcript, error) {
	var all []transcript
	sc := bufio.NewScanner(bytes.NewReader(report))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		key, value, _ := strings.Cut(sc.Text(), " ")
		if key == "transcript" {
			all = append(all, transcript{
				id:      strings.TrimSuffix(path.Base(value), ".jsonl"),
				project: path.Base(path.Dir(value)),
			})
			continue
		}
		if len(all) == 0 {
			return nil, fmt.Errorf("claude: conversations: %q before any transcript", key)
		}
		t := &all[len(all)-1]
		var err error
		switch key {
		case "updated":
			t.updated, err = readSeconds(value)
		case "cwd":
			var cwd string
			cwd, err = readMember(value, "cwd")
			t.cwds = append(t.cwds, cwd)
		case "custom-title":
			t.customTitle, err = readMember(value, "customTitle")
		case "ai-title":
			t.aiTitle, err = readMember(value, "aiTitle")
		default:
			err = fmt.Errorf("unknown line %q", key)
		}
		if err != nil {
			return nil, fmt.Errorf("claude: conversations: transcript %s: %w", t.id, err)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("claude: conversations: %w", err)
	}
	return slices.DeleteFunc(all, func(t transcript) bool { return !isSessionID(t.id) }), nil
}

// readSeconds reads a file's last change in seconds since the epoch; none
// reported is a zero time.
func readSeconds(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	s, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("last change %q: %w", value, err)
	}
	return time.Unix(s, 0).UTC(), nil
}

// readMember reads one JSON member as Claude wrote it into a transcript.
func readMember(member, name string) (string, error) {
	var object map[string]string
	if err := json.Unmarshal([]byte("{"+member+"}"), &object); err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	value, ok := object[name]
	if !ok {
		return "", fmt.Errorf("no %s in %q", name, member)
	}
	return value, nil
}
