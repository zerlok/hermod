# Reports Claude's config directory, and the conversations Claude keeps and
# runs on this workstation.
#
#   sh -c "$(cat conversations.sh)" conversations config
#   sh -c "$(cat conversations.sh)" conversations list PREFIX
#   sh -c "$(cat conversations.sh)" conversations locate [ID]
#
# Claude's config directory is the one Claude here uses,
# ${CLAUDE_CONFIG_DIR:-$HOME/.claude}, absolute; `config` prints it. PREFIX is
# the name Claude gives a directory under its projects/, and every transcript
# of a directory whose name starts with it is reported: those of the
# directories under it start with it too. ID is a conversation's UUID, whose
# transcript is reported wherever it is; without one no transcript is.
#
# Every operation prints a line `conversations:` first, so a reader can tell
# its report from what a login profile printed around it. Each transcript is
# reported as a line `transcript PATH`, a line
# `updated SECONDS` with its last change, a line `cwd MEMBER` for each distinct
# directory it ran in, in the order it first ran there, and, when the
# transcript has them, lines `custom-title MEMBER` and `ai-title MEMBER` with
# the last title the user and Claude gave it; each MEMBER is a JSON member as
# Claude wrote it. A transcript can name more than one directory: one begun on
# another workstation names that one's directory until it is resumed here. A
# line `agents:` and the conversations Claude runs, as `claude agents --json`
# lists them, follow.
c=${CLAUDE_CONFIG_DIR:-$HOME/.claude}
op=$1
shift
case $c in
/*) ;;
*)
  printf 'config directory %s is not absolute\n' "$c" >&2
  exit 2
  ;;
esac
str='"([^"\\]|\\.)*"'

report() {
  for f in "$@"; do
    [ -f "$f" ] || continue
    printf 'transcript %s\n' "$f"
    printf 'updated %s\n' "$(stat -c %Y "$f" 2>/dev/null || stat -f %m "$f")"
    grep -oE "\"cwd\":$str" "$f" | awk '!seen[$0]++' | sed 's/^/cwd /'
    grep -oE "\"customTitle\":$str" "$f" | tail -n1 | sed 's/^/custom-title /'
    grep -oE "\"aiTitle\":$str" "$f" | tail -n1 | sed 's/^/ai-title /'
  done
}

case $op in
config)
  echo conversations:
  printf '%s\n' "$c"
  exit 0
  ;;
list)
  echo conversations:
  report "$c"/projects/"$1"*/*.jsonl
  ;;
locate)
  echo conversations:
  if [ "$#" -gt 0 ]; then report "$c"/projects/*/"$1".jsonl; fi
  ;;
*)
  printf 'unknown operation %s\n' "$op" >&2
  exit 2
  ;;
esac
echo agents:
exec claude agents --json
