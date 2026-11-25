# Starts a worker: runs the tmux command that makes its detached session, once
# no session holds its key, and puts the worker's directory in place.
#
#   sh -s -- WDIR STAGING REFUSED HAS NEW...
#
# WDIR is the worker's directory under $HOME, and STAGING the directory under
# $HOME where write.sh and stream.sh prepared its files. Once the session is
# made, STAGING replaces WDIR; a start that finds the session taken removes
# STAGING and leaves WDIR alone. HAS is the tmux command, quoted for this shell,
# that exits 0 when the session exists. NEW is the tmux command that makes the
# session, its process pane.sh, and sets its tags in the same call, so no
# reader ever lists it untagged.
#
# Exits REFUSED when the session exists already, and non-zero when it cannot
# start one.
set -eu
wd=$HOME/$1 st=$HOME/$2 refused=$3 has=$4
shift 4

taken() {
  rm -rf "$st"
  echo 'the tmux session exists already' >&2
  exit "$refused"
}

if eval "$has" 2>/dev/null </dev/null; then taken; fi

# A session made between the check above and here is another start's, which
# tmux refuses to duplicate.
if ! "$@" </dev/null; then
  if eval "$has" 2>/dev/null </dev/null; then taken; fi
  rm -rf "$st"
  exit 1
fi
rm -rf "$wd"
mv "$st" "$wd"
