package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strconv"
	"strings"

	"github.com/zerlok/hermod/internal/pathmap"
	"github.com/zerlok/hermod/internal/relay"
	"github.com/zerlok/hermod/internal/worker"
)

// Every name Hermod gives starts with prefix, so its relays, workers and lock
// files are told apart from ones made by hand or under earlier names.
const prefix = "hermod-"

// The Mutagen labels an FS relay carries: the remote lists every workspace on
// it, and the workspace lists its own relays.
const (
	labelRemote    = "hermod-remote"
	labelWorkspace = "hermod-workspace"
)

// The tmux options a worker carries: the workspace it belongs to, by which the
// workspace reads its workers, and the machine it was started from.
const (
	tagWorkspace = "@hermod-workspace"
	tagOrigin    = "@hermod-origin"
)

// maxLabel is the longest label value Mutagen accepts. A relay's name is also
// its workspace's label value, so a name is kept within it.
const maxLabel = 63

// localHashLen is how many hex digits of its local end's hash a relay's name
// carries: one remote root can be joined to several local roots, such as one
// directory's transcripts to two Claude config directories.
const localHashLen = 8

// nameSafe is s with every character Mutagen refuses in a name as a dash.
func nameSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		default:
			return '-'
		}
	}, s)
}

// remoteLabel is the remote's alias as a label value, which may neither start
// nor end with a dash.
func remoteLabel(host string) string {
	v := strings.Trim(nameSafe(host), "-")
	if len(v) > maxLabel {
		v = strings.TrimRight(v[:maxLabel], "-")
	}
	return v
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// relayName is the name of the FS relay between place's two ends on host,
// `hermod-<remote>-<slug>-<local>`. It depends on the ends and the remote
// alone, so every session that needs the same two ends finds the same relay.
// A name that would pass the longest label value has its slug cut short; the
// relay's check of its remote end refuses a session found under a cut name
// that joins another remote root.
func relayName(host string, place pathmap.Place) string {
	tag := "-" + hash(path.Clean(place.Local))[:localHashLen]
	head := prefix + nameSafe(host) + "-" + strings.Trim(nameSafe(strings.ReplaceAll(path.Clean(place.Remote), "/", "-")), "-")
	if room := maxLabel - len(tag); len(head) > room {
		head = strings.TrimRight(head[:room], "-")
	}
	return head + tag
}

// ioName is the name of the IO relay to key's stdio on host. A line stream's
// single reader is enforced by its lock, so every process names it alike.
func ioName(host string, key worker.Key) string {
	return prefix + nameSafe(host) + "-io-" + key.String()
}

// netName is the name of the Net relay from port on host.
func netName(host string, port int) string {
	return prefix + nameSafe(host) + "-net-" + strconv.Itoa(port)
}

// fsLabels are the labels of a relay that workspace makes on host.
func fsLabels(host, workspace string) []relay.Label {
	return []relay.Label{{Key: labelRemote, Value: remoteLabel(host)}, {Key: labelWorkspace, Value: workspace}}
}

// ConversationKey is the key of the worker that runs the agent's conversation
// id, `hermod-<id>`.
func ConversationKey(id string) (worker.Key, error) { return worker.NewKey(prefix + id) }

// adapterKey is the key of the worker that runs the agent program's adapter
// for workspace, `hermod-acp-<workspace>`: one per workspace.
func adapterKey(workspace string) (worker.Key, error) {
	return worker.NewKey(prefix + "acp-" + workspace)
}

// origin is the machine a session was started from as a tag value: letters,
// digits, dots and dashes, "" when nothing of it is left.
func origin(name string) string {
	return strings.Trim(strings.Map(func(r rune) rune {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			return r
		default:
			return '-'
		}
	}, name), "-.")
}

// workerTags are the tags of a worker workspace starts; an origin that reduces
// to nothing leaves its tag out.
func workerTags(workspace, from string) ([]worker.Tag, error) {
	ws, err := worker.NewTag(tagWorkspace, workspace)
	if err != nil {
		return nil, err
	}
	tags := []worker.Tag{ws}
	if o := origin(from); o != "" {
		t, err := worker.NewTag(tagOrigin, o)
		if err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, nil
}

// remoteRoot is where a new workspace joins the local directory dir on the
// remote: a directory below the local home lies at the same path below the
// remote one, the home itself under its base name, and any other directly
// under the remote home as its whole path in one segment. Syncing a whole home
// onto the remote home both ways would carry every dotfile across, and a root
// outside the home may not be writable there.
func remoteRoot(localHome, remoteHome, dir string) string {
	if rel, ok := pathmap.Under(dir, localHome); ok {
		if rel == "." {
			rel = path.Base(dir)
		}
		return path.Join(remoteHome, rel)
	}
	return path.Join(remoteHome, strings.ReplaceAll(strings.Trim(dir, "/"), "/", "-"))
}
