# The process of a worker's tmux session: it sets the worker's environment up
# and executes the worker's command in its place. keep.sh's tmux command runs
# it under the user's login shell, so the command is found on the login PATH.
#
#   sh -c "$(cat pane.sh)" pane WDIR STAGING DIR MARK PID ENV FORM IN OUT ERR KEEP [COMMAND...]
#
# WDIR is the worker's directory under $HOME and STAGING the directory under
# $HOME that keep.sh puts in its place once tmux made the session; the process
# waits until it did. DIR is where the process starts, absolute or under
# $HOME. It sources the file ENV of the worker's directory, then sets the
# variable MARK to that directory, after ENV, which could otherwise overwrite
# it; a close knows a line stream's process by it, since its ID may since be
# another's. FORM is `terminal`, or `stream` for a line stream, which records
# its process ID in the file PID, reads the FIFO IN and appends to the logs OUT
# and ERR, all in the worker's directory. KEEP is the tmux command, quoted for
# this shell, that keeps the pane when the process fails, so whoever joins it
# sees the error and exit status. With no COMMAND the process is the user's
# login shell.
wd=$HOME/$1 st=$HOME/$2 dir=$3 mark=$4 pid=$5 env=$6 form=$7 in=$8 out=$9 err=${10} keep=${11}
shift 11

# Run from inside the pane, where a tmux that refuses it, one older than 3.2,
# fails no start.
eval "$keep" 2>/dev/null </dev/null

i=0
while [ -e "$st" ] && [ "$i" -lt 50 ]; do sleep 0.1; i=$((i + 1)); done
if [ -e "$st" ]; then
  printf 'no worker directory was put in place from %s\n' "$st" >&2
  exit 1
fi

case $dir in
/*) ;;
*) dir=$HOME/$dir ;;
esac
cd "$dir" || exit 1

if [ "$form" = stream ]; then echo "$$" >"$wd/$pid"; fi
. "$wd/$env"
export "$mark=$wd"
[ "$#" -gt 0 ] || set -- "$SHELL" -l
# A line stream's process keeps the pane's tty open on fd 9: with no process
# holding it, tmux takes the pane for dead and hangs up on it.
if [ "$form" = stream ]; then exec "$@" 9<&0 <>"$wd/$in" >>"$wd/$out" 2>>"$wd/$err"; fi
exec "$@"
