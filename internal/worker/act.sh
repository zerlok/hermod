# Acts on a worker that keep.sh started: lists the tmux sessions, closes or
# removes one, or joins its stdio.
#
#   sh -c "$(cat act.sh)" act REFUSED LINGERS UNREADABLE OP [ARG]...
#
# REFUSED is the status a refused operation exits with, LINGERS that of a close
# whose process outlived the wait, and UNREADABLE that of a tmux that could not
# tell which sessions exist. OP is one of:
#
#   list FORMAT                 every session in FORMAT, then `end`
#   close TARGET WDIR PID MARK  hang up on a Running worker
#   remove TARGET               remove an Exited worker's kept pane
#   attach TARGET               join a terminal worker on the caller's tty
#   feed FIFO                   copy stdin into a line stream's FIFO
#   log TARGET LOG POS          deliver a line stream's log
#
# TARGET names the session exactly, and WDIR, FIFO, LOG and POS are under
# $HOME. A command it is sent with runs here unchanged, so it reads the same in
# the repository as on the remote.
refused=$1 lingers=$2 unreadable=$3 op=$4
shift 4

# absent reports whether tmux's message says the session asked for, or its
# whole server, is not there. Any other failure may hide a session, so it is no
# answer.
absent() {
  case $1 in
  *"can't find session"* | *"no server running"* | *"error connecting to"*"(No such file or directory)"*) return 0 ;;
  esac
  return 1
}

# present reports whether the session $1 exists, leaving its pane_dead in
# $dead, and exits UNREADABLE when tmux cannot tell. has-session decides, since
# display-message prints an empty line for a missing session while a server
# runs.
present() {
  if ! out=$(tmux has-session -t "$1" 2>&1 </dev/null); then
    absent "$out" && return 1
    printf '%s\n' "$out" >&2
    exit "$unreadable"
  fi
  dead=$(tmux display-message -p -t "$1" '#{pane_dead}' </dev/null) || exit "$unreadable"
  [ -n "$dead" ]
}

# A look that ends with `end` is complete. A tmux with no server has no
# sessions; any other failure exits UNREADABLE, since sessions may exist that
# it could not list.
list() {
  if ! out=$(tmux list-sessions -F "$1" 2>&1 </dev/null); then
    if ! absent "$out"; then
      printf '%s\n' "$out" >&2
      exit "$unreadable"
    fi
    out=
  fi
  if [ -n "$out" ]; then printf '%s\n' "$out"; fi
  echo end
}

# Hangs up on the worker's process by ending its tmux session, so a program
# such as an agent saves its state and exits, and waits up to 5 s for the
# process whose ID a line stream's start recorded to be gone, so a start that
# follows never runs beside it. A process counts only while its environment
# carries the worker's mark: the ID may since be another's. A session already
# gone has nothing to close; an Exited one is refused.
close() {
  present "$1" || exit 0
  d=$HOME/$2
  p=$(cat "$d/$3" 2>/dev/null) || p=
  if [ "$dead" = 1 ]; then exit "$refused"; fi
  tmux kill-session -t "$1" </dev/null 2>/dev/null || true
  i=0
  while ours "$p" "$4=$d" && [ "$i" -lt 50 ]; do sleep 0.1; i=$((i + 1)); done
  if ours "$p" "$4=$d"; then exit "$lingers"; fi
}

ours() { [ -n "$1" ] && tr '\0' '\n' 2>/dev/null <"/proc/$1/environ" | grep -qxF "$2"; }

# Removes an Exited worker's kept pane, and refuses a Running one.
remove() {
  present "$1" || exit 0
  if [ "$dead" != 1 ]; then exit "$refused"; fi
  exec tmux kill-session -t "$1" </dev/null
}

# Joins a session only when it exists: attaching creates nothing, but it fails
# without saying why.
attach() {
  tmux has-session -t "$1" 2>/dev/null </dev/null || {
    printf 'no tmux session %s\n' "$1" >&2
    exit "$refused"
  }
  exec tmux attach-session -t "$1"
}

# Copies stdin into the FIFO one complete line at a time: `read -r` gives a
# line only once its newline came, so a connection dropped mid-line hands the
# process no part of it. Opening the FIFO for reading too never waits on a
# reader.
feed() {
  f=$HOME/$1
  [ -p "$f" ] || {
    printf 'no line stream at %s\n' "$f" >&2
    exit "$refused"
  }
  exec 3<>"$f"
  while IFS= read -r l; do printf '%s\n' "$l"; done >&3
}

# Delivers one log from the position its last reader recorded, one complete
# line at a time, recording the position after each line, so what the process
# wrote while nobody read is delivered once. It follows the log while the
# process lives; once the pane is gone or dead, it stops following and
# delivers what is left from the recorded position, so the process's last
# lines are not lost with the follower. A tmux that cannot tell counts as the
# process living, so the stream never ends on a guess.
#
# A failed write means the reader is gone: it ends every process it started,
# all in its process group, which sshd makes its own, since a follower left
# behind would poll the log for nobody.
log() {
  export LC_ALL=C
  trap '' PIPE
  t=$1 log=$HOME/$2 pos=$HOME/$3
  [ -f "$log" ] || {
    printf 'no log at %s\n' "$log" >&2
    exit "$refused"
  }
  p=$(at)
  {
    tail -c +"$((p + 1))" -f "$log" </dev/null &
    f=$!
    while alive; do sleep 1 </dev/null >/dev/null 2>&1; done
    kill "$f" 2>/dev/null
    wait "$f" 2>/dev/null
  } | emit "$p"
  p=$(at)
  tail -c +"$((p + 1))" "$log" </dev/null | emit "$p"
  exit 0
}

alive() {
  if s=$(tmux display-message -p -t "$t" '#{pane_dead}' 2>&1 </dev/null); then
    case $s in 1 | "") return 1 ;; esac
  else
    absent "$s" && return 1
  fi
  return 0
}

emit() {
  p=$1
  while IFS= read -r l; do
    printf '%s\n' "$l" || kill 0
    p=$((p + ${#l} + 1))
    echo "$p" >"$pos"
  done
}

at() { cat "$pos" 2>/dev/null || echo 0; }

case $op in
list | close | remove | attach | feed | log) "$op" "$@" ;;
*)
  printf 'unknown operation %s\n' "$op" >&2
  exit 2
  ;;
esac
