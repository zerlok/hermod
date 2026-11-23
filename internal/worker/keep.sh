# Starts a worker: its process in a detached tmux session whose pane tmux keeps
# when the process fails, so whoever joins it sees the error and exit status.
#
#   sh -s -- SESSION WDIR STAGING DIR MARK PID ENV FORM IN OUT ERR REFUSED NTAGS [OPTION VALUE]... [COMMAND...]
#
# SESSION is the tmux session. WDIR is the worker's directory under $HOME, and
# STAGING the directory under $HOME where write.sh and stream.sh prepared its
# files, among them ENV, which the process sources. Once the session is made,
# STAGING replaces WDIR; a start that finds the session taken removes STAGING
# and leaves WDIR alone. DIR is where the process starts, absolute or under
# $HOME. FORM is `terminal`, or `stream` for a line stream, which reads the FIFO
# IN and appends to the logs OUT and ERR, all in the worker's directory; a
# line stream's process records its ID in the file PID there, and carries the
# variable MARK set to the directory. NTAGS OPTION VALUE pairs follow, each a
# tmux user option set on the session. With no COMMAND the process is the
# user's login shell.
#
# Exits REFUSED when the session exists already, and non-zero when it cannot
# start one.
set -eu
s=$1 wd=$HOME/$2 st=$HOME/$3 dir=$4 mark=$5 pid=$6 env=$7 form=$8 in=$9 out=${10} err=${11} refused=${12} n=${13}
shift 13
case $dir in
/*) ;;
*) dir=$HOME/$dir ;;
esac
# tmux reads -c as a format, where '#' starts an expansion.
dir=$(printf '%s\n' "$dir" | sed 's/#/##/g')

taken() {
  rm -rf "$st"
  printf 'tmux session %s exists already\n' "$s" >&2
  exit "$refused"
}

if tmux has-session -t "=$s" 2>/dev/null </dev/null; then taken; fi

# The pane's program, run by the user's login shell so the command is found on
# the login PATH. It asks tmux to keep the pane when the process fails from
# inside the pane, where a tmux older than 3.2, which lacks `failed`, refuses
# it without failing the start. It waits until this script has put the
# worker's directory in place, which it does only after tmux made the session.
# A line stream's process records its ID, which a close waits on, and marks its
# environment, so a close never waits on another process that reused the ID.
# Its own variables are set after the variables file is sourced, which could
# otherwise overwrite them. A line stream's process keeps the pane's tty open
# on fd 9: with no process holding it, tmux takes the pane for dead and hangs
# up on it.
pane='tmux set-option -w remain-on-exit failed 2>/dev/null </dev/null
i=0
while [ -e "$2" ] && [ "$i" -lt 50 ]; do sleep 0.1; i=$((i + 1)); done
if [ -e "$2" ]; then printf "no worker directory was put in place from %s\n" "$2" >&2; exit 1; fi
if [ "$6" = stream ]; then echo "$$" > "$1/$4"; fi
. "$1/$5"
if [ "$6" = stream ]; then export "$3=$1"; fi
d=$1 form=$6 in=$7 out=$8 err=$9
shift 9
[ "$#" -gt 0 ] || set -- "$SHELL" -l
if [ "$form" = stream ]; then exec "$@" 9<&0 <>"$d/$in" >>"$d/$out" 2>>"$d/$err"; fi
exec "$@"'

# Each tag becomes a `; set-option` after the command, in the same tmux call,
# so no reader ever lists the session untagged. The pairs lead the arguments:
# each turn appends one option and shifts its pair off the front.
i=0
while [ "$i" -lt "$n" ]; do
  set -- "$@" ';' set-option -t "=$s:" -qo "$1" "$2"
  shift 2
  i=$((i + 1))
done

# Given as several arguments, the command is executed without a shell
# (tmux 3.0 and later), so its words reach the process as they are. A session
# made between the check above and here is another start's, which tmux refuses
# to duplicate.
if ! tmux new-session -d -s "$s" -c "$dir" -- "$SHELL" -lc "$pane" hermod \
  "$wd" "$st" "$mark" "$pid" "$env" "$form" "$in" "$out" "$err" "$@" </dev/null; then
  if tmux has-session -t "=$s" 2>/dev/null </dev/null; then taken; fi
  rm -rf "$st"
  exit 1
fi
rm -rf "$wd"
mv "$st" "$wd"
