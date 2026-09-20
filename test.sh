#!/usr/bin/env bash
#
# test.sh - Unit-test runner for the WasmCompiler / TinygoCompiler rework.
#
# Usage:
#   ./test.sh              run vet + devutil unit tests + cross-compile checks
#   ./test.sh -v           run tests with -v (verbose)
#   ./test.sh -r <name>    run only tests matching <name>
#
# Notes:
# - The affected code (devutil) only depends on the Go standard library, so
#   this script does not require third-party modules to be downloaded.
# - A local GOCACHE is used when the default one is not writable.
set -euo pipefail

cd "$(dirname "$0")"

VERBOSE=""
RUN_PATTERN="TestWasmCompiler|TestTinygoCompiler|TestRunCmdStreaming|TestHelperProcess|TestFileServer|TestMux"
while getopts "vr:h" opt; do
  case "$opt" in
    v) VERBOSE="-v" ;;
    r) RUN_PATTERN="$OPTARG" ;;
    h)
      sed -n '2,14p' "$0"
      exit 0
      ;;
    *) exit 2 ;;
  esac
done

if ! command -v go >/dev/null 2>&1; then
  echo "ERROR: go toolchain not found in PATH" >&2
  exit 1
fi

# Use a writable cache when the default one is not (sandboxes, CI containers).
DEFAULT_GOCACHE="$(go env GOCACHE)"
if [ -z "${GOCACHE:-}" ]; then
  PROBE="$DEFAULT_GOCACHE/.write-probe.$$"
  if mkdir -p "$DEFAULT_GOCACHE" 2>/dev/null && touch "$PROBE" 2>/dev/null; then
    rm -f "$PROBE"
  else
    export GOCACHE="/tmp/vugu-gocache"
    echo "Default GOCACHE ($DEFAULT_GOCACHE) not writable, using $GOCACHE"
  fi
fi

echo "== go version =="
go version

echo
echo "== go vet ./devutil/ =="
go vet ./devutil/

echo
echo "== unit tests: devutil =="
# shellcheck disable=SC2086
go test ./devutil/ -count=1 $VERBOSE -run "$RUN_PATTERN"

echo
echo "== race detector: compiler tests =="
# shellcheck disable=SC2086
go test ./devutil/ -count=1 -race $VERBOSE -run "$RUN_PATTERN"

echo
echo "== cross-compile: windows/amd64, linux/arm64 =="
GOOS=windows GOARCH=amd64 go build ./devutil/
GOOS=linux   GOARCH=arm64 go build ./devutil/

echo
echo "ALL CHECKS PASSED"
