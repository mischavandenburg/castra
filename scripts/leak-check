#!/usr/bin/env bash
# Fails when any pattern in $LEAK_PATTERNS appears anywhere in the tree.
#
# LEAK_PATTERNS holds one case-insensitive extended regex per line (# starts
# a comment). The patterns are private names by design, so they never live
# in this repository: CI reads them from the LEAK_PATTERNS secret. Matches
# are reported as file:line only, so the log never repeats the matched text.
set -euo pipefail

dir="${1:-.}"
if [ -z "${LEAK_PATTERNS:-}" ]; then
  echo "leak-check: LEAK_PATTERNS is empty; refusing to pass without patterns" >&2
  exit 2
fi

patterns="$(mktemp)"
trap 'rm -f "$patterns"' EXIT
printf '%s\n' "$LEAK_PATTERNS" | sed -e '/^[[:space:]]*#/d' -e '/^[[:space:]]*$/d' > "$patterns"
if [ ! -s "$patterns" ]; then
  echo "leak-check: LEAK_PATTERNS has no patterns; refusing to pass" >&2
  exit 2
fi

if hits="$(grep -rIniE --exclude-dir=.git -f "$patterns" "$dir" | cut -d: -f1,2)"; then
  echo "leak-check: private names found at:" >&2
  printf '%s\n' "$hits" >&2
  exit 1
fi
echo "leak-check: clean ($(wc -l < "$patterns") patterns)"
