package cli

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// sshHostCompletions offers concrete host aliases from ~/.ssh/config for the
// single sandbox argument, so the sandbox is tab-completable as the README
// advertises. Only the first positional argument is completed.
func sshHostCompletions(home func() (string, error)) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		h, err := home()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		f, err := os.Open(filepath.Join(h, ".ssh", "config"))
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		defer f.Close()
		return parseSSHHosts(f), cobra.ShellCompDirectiveNoFileComp
	}
}

// parseSSHHosts extracts concrete Host aliases from an ssh_config, skipping
// pattern entries (those containing '*' or '?') that cannot be connected to
// directly.
func parseSSHHosts(r io.Reader) []string {
	seen := map[string]struct{}{}
	var hosts []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || !strings.EqualFold(fields[0], "Host") {
			continue
		}
		for _, alias := range fields[1:] {
			if strings.ContainsAny(alias, "*?") {
				continue
			}
			if _, dup := seen[alias]; dup {
				continue
			}
			seen[alias] = struct{}{}
			hosts = append(hosts, alias)
		}
	}
	sort.Strings(hosts)
	return hosts
}
