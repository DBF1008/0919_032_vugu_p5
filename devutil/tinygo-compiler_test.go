//go:build !windows

package devutil

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func newTestTinygoCompiler(t *testing.T) *TinygoCompiler {
	t.Helper()
	c, err := NewTinygoCompiler()
	if err != nil {
		t.Fatal(err)
	}
	return c.NoDocker().SetLogWriter(&bytes.Buffer{}).
		SetBuildCmdFunc(func(outpath string) *exec.Cmd {
			return helperProcess(t, "touch", outpath)
		})
}

func TestTinygoCompilerExecuteSuccess(t *testing.T) {
	var log bytes.Buffer
	wc, err := NewTinygoCompiler()
	if err != nil {
		t.Fatal(err)
	}
	wc.NoDocker().SetLogWriter(&log).
		SetBuildCmdFunc(func(outpath string) *exec.Cmd {
			return helperProcess(t, "touch", outpath)
		})

	outpath, err := wc.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer os.Remove(outpath)
	if b, err := os.ReadFile(outpath); err != nil || string(b) != "wasm-bytes" {
		t.Fatalf("output file content wrong: %q, %v", b, err)
	}
	if !strings.Contains(log.String(), "successful build") {
		t.Fatalf("log missing build confirmation:\n%s", log.String())
	}
}

func TestTinygoCompilerCanceled(t *testing.T) {
	wc, err := NewTinygoCompiler()
	if err != nil {
		t.Fatal(err)
	}
	wc.NoDocker().SetLogWriter(&bytes.Buffer{}).
		SetBuildCmdFunc(func(outpath string) *exec.Cmd {
			return helperProcess(t, "sleep")
		})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	before := countWasmCompilerTmps(t)
	_, err = wc.ExecuteContext(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if got := countWasmCompilerTmps(t); got != before {
		t.Fatalf("temp file leaked on cancel: before=%d after=%d", before, got)
	}
}

func TestTinygoCompilerTempCleanedOnFailure(t *testing.T) {
	wc := newTestTinygoCompiler(t).
		SetBuildCmdFunc(func(outpath string) *exec.Cmd {
			return helperProcess(t, "fail")
		})

	before := countWasmCompilerTmps(t)
	_, err := wc.Execute()
	if err == nil {
		t.Fatal("expected build error")
	}
	if !strings.Contains(err.Error(), "boom: something failed") {
		t.Fatalf("error missing command output: %v", err)
	}
	if got := countWasmCompilerTmps(t); got != before {
		t.Fatalf("temp file leaked: before=%d after=%d", before, got)
	}
}

func TestTinygoCompilerAfterFuncError(t *testing.T) {
	afterErr := errors.New("tinygo after failed")
	wc := newTestTinygoCompiler(t).SetAfterFunc(func(outpath string, buildErr error) error {
		return afterErr
	})

	outpath, err := wc.Execute()
	if !errors.Is(err, afterErr) {
		t.Fatalf("expected afterErr, got: %v", err)
	}
	if outpath != "" {
		t.Fatalf("outpath must be empty on error, got %q", outpath)
	}
}

func TestTinygoCompilerBeforeFuncError(t *testing.T) {
	wc := newTestTinygoCompiler(t).
		SetBeforeFunc(func() error { return errors.New("tinygo before boom") })

	_, err := wc.Execute()
	if err == nil || !strings.Contains(err.Error(), "tinygo before boom") {
		t.Fatalf("expected before func error, got: %v", err)
	}
}

func TestTinygoCompilerNoBuildCmd(t *testing.T) {
	wc, err := NewTinygoCompiler()
	if err != nil {
		t.Fatal(err)
	}
	wc.NoDocker()
	if _, err := wc.Execute(); err == nil {
		t.Fatal("expected error when no build command is set")
	}
}
