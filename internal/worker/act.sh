# Acts on a worker that keep.sh started: lists the tmux sessions, closes or
# removes one, or joins its stdio.
#
#   sh -c "$(cat act.sh)" act REFUSED LINGERS UNREADABLE ABSENT OP [ARG]...
#
# REFUSED is the status a refused operation exits with, LINGERS that of a close
# whose process outlived the wait, and UNREADABLE that of a tmux that could not
# tell which sessions exist. ABSENT is the status HAS and DEAD exit with for a
# missing session. OP is one of:
#
#   list LIST                       every session as LIST prints it, then `end`
#   close DEAD KILL WDIR PID MARK   hang up on a Running worker
#   remove DEAD KILL                remove an Exited worker's kept pane
#   attach HAS ATTACH               join a terminal worker on the caller's tty
#   feed FIFO                       copy stdin into a line stream's FIFO
#   log DEAD LOG POS                deliver a line stream's log
#   look DEAD WDIR LINE [FILE]...   read a worker's state and files, and run LINE
#   port DIR RECORD [PORT]...       pick a port free on the remote
#
# HAS, DEAD, KILL, ATTACH and LIST are tmux commands on the worker's session,
# each quoted for this shell: HAS exits 0 when the session exists, DEAD prints 1
# when its pane is kept after the process ended and 0 while it runs, KILL ends
# it, ATTACH joins it, and LIST lists every session, printing nothing when there
# is none. HAS, DEAD and LIST exit ABSENT, or 0 for LIST, when the session or
# the tmux server is missing, and with another failure when tmux cannot tell,
# having said why on stderr. LINE is a command quoted for this shell. WDIR,
# FIFO, LOG and POS are under $HOME. A command it is sent with runs here
# unchanged, so it reads the same in the repository as on the remote.
refused=$1 lingers=$2 unreadable=$3 absent=$4 op=$5
shift 5

# present reports whether the session exists, by DEAD $1, leaving what it
# prints in $dead, and exits UNREADABLE when tmux cannot tell.
present() {
  dead=$(eval "$1" </dev/null)
  case $? in
  0) return 0 ;;
  "$absent") return 1 ;;
  esac
  exit "$unreadable"
}

# A look that ends with `end` is complete; one that fails exits UNREADABLE,
# since sessions may exist that it could not list.
list() {
  out=$(eval "$1" </dev/null) || exit "$unreadable"
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
  d=$HOME/$3
  p=$(cat "$d/$4" 2>/dev/null) || p=
  if [ "$dead" = 1 ]; then exit "$refused"; fi
  eval "$2" </dev/null 2>/dev/null || true
  i=0
  while ours "$p" "$5=$d" && [ "$i" -lt 50 ]; do sleep 0.1; i=$((i + 1)); done
  if ours "$p" "$5=$d"; then exit "$lingers"; fi
}

ours() { [ -n "$1" ] && tr '\0' '\n' 2>/dev/null <"/proc/$1/environ" | grep -qxF "$2"; }

# Removes an Exited worker's kept pane, and refuses a Running one.
remove() {
  present "$1" || exit 0
  if [ "$dead" != 1 ]; then exit "$refused"; fi
  eval "$2" </dev/null
}

# Joins a session only when it exists: attaching creates nothing, but it fails
# without saying why.
attach() {
  if ! eval "$1" </dev/null; then
    echo 'no tmux session to join' >&2
    exit "$refused"
  fi
  eval "exec $2"
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
  pane=$1 log=$HOME/$2 pos=$HOME/$3
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
  s=$(eval "$pane" 2>/dev/null </dev/null)
  case $? in
  0) [ "$s" != 1 ] ;;
  "$absent") return 1 ;;
  *) return 0 ;;
  esac
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

# Reads in one call what a caller decides from: the worker's state, by DEAD,
# as `state 0`, `state 1` for a dead pane or `state absent`; each FILE of its
# directory that exists, as `file NAME LENGTH` and its bytes; then `out` and
# what the command LINE writes. A LINE that fails fails the look, since its
# output then answers nothing.
look() {
  pane=$1 wd=$HOME/$2 line=$3
  shift 3
  if present "$pane"; then printf 'state %s\n' "$dead"; else echo 'state absent'; fi
  for f; do
    if [ -f "$wd/$f" ]; then
      printf 'file %s %s\n' "$f" "$(wc -c <"$wd/$f" | tr -d ' ')"
      cat "$wd/$f"
    fi
  done
  echo out
  if [ -n "$line" ]; then eval "exec $line" </dev/null; fi
}

# Prints the first PORT that no worker's directory under DIR records in its
# file RECORD and no socket uses, as the tcp and tcp6 tables under NET (Linux's
# /proc/net) list them. A detached worker's port is bound by nothing, so only
# its record keeps it from another worker. Exits REFUSED when every PORT is
# taken. grep reads each record as its own input, since records joined by cat
# run together when one lacks its final newline.
port() {
  d=$HOME/$1 r=$2 n=$3
  shift 3
  for p; do
    if grep -qsxF -- "$p" "$d"/*/"$r"; then continue; fi
    if cat "$n"/tcp "$n"/tcp6 2>/dev/null | grep -q ":$(printf '%04X' "$p") "; then continue; fi
    echo "$p"
    exit 0
  done
  exit "$refused"
}

case $op in
list | close | remove | attach | feed | log | look | port) "$op" "$@" ;;
*)
  printf 'unknown operation %s\n' "$op" >&2
  exit 2
  ;;
esac
