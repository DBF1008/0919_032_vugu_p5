#!/usr/bin/env bash
#
# test.sh runs the devutil compiler unit tests added for the WasmCompiler /
# TinygoCompiler Execute refactor (context cancellation, temp-file cleanup,
# unified error propagation and live log streaming).
#
# Usage: ./test.sh
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

echo "==> gofmt check"
unformatted="$(gofmt -l devutil/compiler.go devutil/tinygo-compiler.go devutil/execcmd.go devutil/execcmd_unix.go devutil/execcmd_windows.go devutil/compiler_execute_test.go devutil/tinygo_compiler_execute_test.go devutil/handlers.go)"
if [ -n "$unformatted" ]; then
	echo "The following files are not gofmt-clean:"
	echo "$unformatted"
	exit 1
fi
echo "ok"

echo "==> go vet"
go vet ./devutil

echo "==> unit tests (race detector)"
go test ./devutil -race -count=1 -v

echo "==> all devutil compiler tests passed"
