# Writes a worker's files into a new staging directory, which keep.sh puts in
# place of the worker's directory only once the worker's tmux session is its
# own, so a start that loses a race to another never touches the files of the
# worker that won.
#
#   sh -c "$(cat write.sh)" write STAGING [NAME LENGTH]...
#
# STAGING is the staging directory under $HOME, which must not exist yet. The
# files' contents come one after another on stdin, each NAME taking the next
# LENGTH bytes. The directory and its parents are private to the remote user.
set -eu
umask 077
d=$HOME/$1
shift
mkdir -p "${d%/*}"
mkdir "$d"
cd "$d"
cat > .incoming
o=1
while [ "$#" -gt 0 ]; do
  tail -c +"$o" .incoming | head -c "$2" > "$1"
  o=$((o + $2))
  shift 2
done
rm -f .incoming
