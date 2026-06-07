#!/bin/sh
# Fake `pi` for runner tests. Contract:
#  - stdin MUST be empty (/dev/null). If any byte arrives, write FAIL marker and exit 3.
#  - Record argv, cwd, and selected env vars into $FAKE_PI_SENTINEL (one per line).
#  - If last arg contains the token __HANG__, sleep forever (for context-kill test).
#  - Otherwise cat the fixture stream from $FAKE_PI_FIXTURE to stdout, exit 0.

SENTINEL="${FAKE_PI_SENTINEL:-/dev/null}"

# 1) Assert stdin is empty. Read with a 0-ish timeout-free approach: if stdin is
#    /dev/null, `head -c 1` returns nothing. If something is piped, it returns a byte.
first_byte=$(head -c 1 2>/dev/null)
if [ -n "$first_byte" ]; then
  printf 'STDIN_NOT_EMPTY\n' >> "$SENTINEL"
  exit 3
fi
printf 'STDIN_EMPTY_OK\n' >> "$SENTINEL"

# 2) Record cwd.
printf 'CWD=%s\n' "$(pwd)" >> "$SENTINEL"

# 3) Record argv (one ARG= line each, in order).
for a in "$@"; do
  printf 'ARG=%s\n' "$a" >> "$SENTINEL"
done

# 4) Record selected env passthrough markers.
printf 'ENV_FAKE_PI_PROBE=%s\n' "${FAKE_PI_PROBE:-<unset>}" >> "$SENTINEL"
printf 'ENV_HOME=%s\n' "${HOME:-<unset>}" >> "$SENTINEL"

# 5) Hang mode for context-kill test: detect __HANG__ anywhere in args.
for a in "$@"; do
  case "$a" in
    *__HANG__*)
      printf 'HANGING\n' >> "$SENTINEL"
      # Block indefinitely; the test cancels the context to kill us.
      while true; do sleep 1; done
      ;;
  esac
done

# 6) Emit the fixture stream.
if [ -n "$FAKE_PI_FIXTURE" ] && [ -f "$FAKE_PI_FIXTURE" ]; then
  cat "$FAKE_PI_FIXTURE"
fi
exit 0
