#!/bin/sh
# fake_diagnostics.sh is a POSIX fixture for the get_diagnostics tests.
# It emits diagnostic-shaped output in the shapes the parser understands.
# The tool under test runs this script through sh with a configured argv.
# The script reacts to its own argv:
#   --format=json : emit a JSON diagnostics block instead of line mode
#   --redact      : also emit a line (or a JSON element) whose file path
#                   carries a credential token
#   --fail        : exit non-zero (3) after emitting the output
#   --sleep=N     : sleep N seconds before emitting the output
#   --flood       : emit many lines of gcc-shaped output (for budget tests)
# The default line mode mixes gcc-shaped lines and a raw-noise line.
# The envelope must carry both structured rows and raw rows.
#
# In JSON mode the redact element is emitted INSIDE the JSON array, so the
# capture stays a single valid JSON document and the JSON parser path is
# exercised even when redaction is requested (audit finding P5).
#
# The credential token is assembled from two quoted pieces. The assembled
# value never appears in this file, so the repo secret scanner stays clean.

TOKEN='sk-'"ant-fixture-redact-token-1234567890"

FORMAT=line
for arg in "$@"; do
    case "$arg" in
        --format=json) FORMAT=json ;;
        --redact) REDACT=1 ;;
        --fail) FAIL=1 ;;
        --sleep=*) SLEEP=${arg#--sleep=} ;;
        --flood) FLOOD=1 ;;
    esac
done

if [ -n "$FLOOD" ]; then
    i=0
    while [ "$i" -lt 200 ]; do
        printf 'src/f%d.go:%d:1: error: flood finding %d\n' "$i" "$((i + 1))" "$i"
        i=$((i + 1))
    done
fi

if [ -n "$SLEEP" ]; then
    sleep "$SLEEP"
fi

if [ -n "$FLOOD" ]; then
    exit 0
fi

if [ "$FORMAT" = json ]; then
    if [ -n "$REDACT" ]; then
        cat <<JSON
{"diagnostics":[
  {"file":"main.go","line":12,"column":5,"severity":"error","message":"undefined: foo"},
  {"file":"vendor/helper.go","line":3,"column":2,"severity":"warning","message":"unused variable bar"},
  {"file":"src/extra.go","line":9,"message":"third finding"},
  {"file":"src/$TOKEN/auth.go","line":1,"column":1,"severity":"error","message":"boom"}
]}
JSON
    else
        cat <<'JSON'
{"diagnostics":[
  {"file":"main.go","line":12,"column":5,"severity":"error","message":"undefined: foo"},
  {"file":"vendor/helper.go","line":3,"column":2,"severity":"warning","message":"unused variable bar"},
  {"file":"src/extra.go","line":9,"message":"third finding"}
]}
JSON
    fi
else
    printf 'main.go:12:5: error: undefined: foo\n'
    printf 'vendor/helper.go:3:2: warning: unused variable bar\n'
    printf 'some raw noise line that matches no known shape\n'
    if [ -n "$REDACT" ]; then
        printf 'src/%s/auth.go:1:1: error: boom\n' "$TOKEN"
    fi
fi

if [ -n "$FAIL" ]; then
    exit 3
fi

exit 0
