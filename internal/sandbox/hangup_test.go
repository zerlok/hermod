package sandbox

import (
	"bufio"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// hangUpLinger is how long a fake Claude takes to exit after its hang-up, as
// a real one takes to save its transcript; noWait is how long a script that
// returns at once may take.
const (
	hangUpLinger = 1500 * time.Millisecond
	noWait       = 300 * time.Millisecond
)

// fakeClaude starts a process at the pid the session directory records. When
// ours, it stands for the session's Claude: it carries HERMOD_SESSION and exits
// hangUpLinger after a SIGHUP. Otherwise it stands for another process the pid
// was reused by, which ignores the hang-up. gone is closed once it has exited
// and been reaped.
func fakeClaude(t *testing.T, sh string, ours bool) (pid int, gone <-chan struct{}) {
	t.Helper()
	linger := strconv.FormatFloat(hangUpLinger.Seconds(), 'f', 1, 64)
	trap := `trap '' HUP`
	if ours {
		trap = `trap 'sleep ` + linger + `; exit 0' HUP`
	}
	// It says when its trap is set: a hang-up before then would end it at once.
	cmd := osexec.Command(sh, "-c", trap+`; echo ready; while :; do sleep 0.05; done`)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	if ours {
		cmd.Env = append(cmd.Env, "HERMOD_SESSION="+agentUUID)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(out).ReadString('\n'); err != nil {
		t.Fatalf("fake Claude: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	return cmd.Process.Pid, done
}

// fakeTools puts a tmux and a claude on PATH: the tmux reports the session as
// tmuxState says and a dead pane pid, and hangs up on the recorded pid when the
// session is killed; the claude lists no session.
func fakeTools(t *testing.T, home, tmuxState string) string {
	t.Helper()
	bin := t.TempDir()
	has := "exit 0"
	if tmuxState == "gone" {
		has = "exit 1"
	}
	tools := map[string]string{
		"tmux": `#!/bin/sh
case "$1" in
has-session) ` + has + `;;
display-message) echo 2147483646;;
kill-session) kill -HUP "$(cat "` + home + `/.hermod/s/` + agentUUID + `/pid")" 2>/dev/null; exit 0;;
esac`,
		"claude": "#!/bin/sh\necho '[]'",
	}
	for name, body := range tools {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return bin
}

// TestHangUpWaitsForClaude runs the close and the probe in a POSIX shell and
// asserts each waits for the recorded Claude of the session while its tmux
// session is going, and not for a process the pid was reused by.
func TestHangUpWaitsForClaude(t *testing.T) {
	cases := []struct {
		name      string
		script    string
		tmux      string // "running" or "gone"
		ours      bool   // the recorded pid is the session's Claude
		wantGone  bool   // the recorded process has exited when the script ends
		wantClose bool   // the script exits 0
	}{
		{"close waits for Claude, not the pane's shell", closeScript, "running", true, true, true},
		{"close passes over a reused pid", closeScript, "running", false, false, true},
		{"probe waits for a Claude being hung up on", probeScript, "gone", true, true, true},
		{"probe passes over a reused pid", probeScript, "gone", false, false, true},
	}
	sh, err := osexec.LookPath("sh")
	if err != nil {
		t.Skip("no POSIX shell")
	}
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("no /proc")
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			dir := filepath.Join(home, ".hermod", "s", agentUUID)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			pid, gone := fakeClaude(t, sh, tc.ours)
			if err := os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.tmux == "gone" {
				// The close has hung up already; the probe comes during its wait.
				if err := osexec.Command("kill", "-HUP", strconv.Itoa(pid)).Run(); err != nil {
					t.Fatal(err)
				}
			}
			cmd := osexec.Command(sh, "-c", tc.script, "sh", agentUUID)
			cmd.Env = []string{"HOME=" + home, "PATH=" + fakeTools(t, home, tc.tmux) + ":" + os.Getenv("PATH")}
			err := cmd.Run()
			if (err == nil) != tc.wantClose {
				t.Errorf("script error = %v, want none %v", err, tc.wantClose)
			}
			select {
			case <-gone:
				if !tc.wantGone {
					t.Error("the script waited for a process that is not the session's Claude")
				}
			case <-time.After(noWait):
				if tc.wantGone {
					t.Error("the script ended while the session's Claude still ran")
				}
			}
		})
	}
}
