# Gives a line-stream worker its FIFO and logs in the staging directory
# write.sh made, before keep.sh starts it.
#
#   sh -s -- STAGING IN OUT ERR
#
# STAGING is the staging directory under $HOME. The process reads the FIFO IN
# and appends to the logs OUT and ERR there. The logs start empty, so the first
# attach after a start streams this process's output from its first line.
set -eu
umask 077
cd "$HOME/$1"
mkfifo "$2"
: >"$3"
: >"$4"
