package devutil_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vugu/vugu/devutil"
)

func newTinygoCompiler(t *testing.T, log *bytes.Buffer) *devutil.TinygoCompiler {
	t.Helper()
	c, err := devutil.NewTinygoCompiler()
	if err != nil {
		t.Fatalf("NewTinygoCompiler: %v", err)
	}
	c.SetLogWriter(log).NoDocker()
	return c
}

func TestTinygoExecute_Success(t *testing.T) {
	unixOnly(t)
	useWritableTempDir(t)
	var log bytes.Buffer
	c := newTinygoCompiler(t, &log)
	c.SetBuildCmdFunc(func(_ context.Context, outpath string) *exec.Cmd {
		return exec.Command("sh", "-c", "printf wasm > \"$1\"", "sh", outpath)
	})

	outpath, err := c.ExecuteContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer os.Remove(outpath)

	if _, err := os.Stat(outpath); err != nil {
		t.Fatalf("output file missing: %v", err)
	}
}

func TestTinygoExecute_BuildError_CleansUp(t *testing.T) {
	unixOnly(t)
	useWritableTempDir(t)
	before := countWasmCompilerTempFiles(t)

	var log bytes.Buffer
	c := newTinygoCompiler(t, &log)
	c.SetBuildCmdFunc(func(_ context.Context, _ string) *exec.Cmd {
		return exec.Command("sh", "-c", "echo tinygo-boom; exit 5")
	})

	outpath, err := c.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "tinygo-boom") {
		t.Fatalf("expected build error with output, got %v", err)
	}
	if outpath != "" {
		t.Fatalf("expected empty outpath, got %q", outpath)
	}
	if got := countWasmCompilerTempFiles(t); got != before {
		t.Fatalf("temp file leaked: before=%d after=%d", before, got)
	}
}

func TestTinygoExecute_AfterFuncIsInvokedAndErrorPropagated(t *testing.T) {
	unixOnly(t)
	useWritableTempDir(t)
	before := countWasmCompilerTempFiles(t)

	var log bytes.Buffer
	c := newTinygoCompiler(t, &log)
	c.SetBuildCmdFunc(func(_ context.Context, outpath string) *exec.Cmd {
		return exec.Command("sh", "-c", "printf wasm > \"$1\"", "sh", outpath)
	})
	called := false
	c.SetAfterFunc(func(_ context.Context, _ string, buildErr error) error {
		called = true
		if buildErr != nil {
			t.Fatalf("expected nil build err in after func, got %v", buildErr)
		}
		return errors.New("tinygo after failed")
	})

	outpath, err := c.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "tinygo after failed") {
		t.Fatalf("after func error should be propagated, got %v", err)
	}
	if outpath != "" {
		t.Fatalf("expected empty outpath, got %q", outpath)
	}
	if !called {
		t.Fatal("afterFunc was not invoked")
	}
	if got := countWasmCompilerTempFiles(t); got != before {
		t.Fatalf("temp file leaked: before=%d after=%d", before, got)
	}
}

func TestTinygoExecute_CancelKillsProcess(t *testing.T) {
	unixOnly(t)
	useWritableTempDir(t)
	before := countWasmCompilerTempFiles(t)

	var log bytes.Buffer
	c := newTinygoCompiler(t, &log)
	pidFile := filepath.Join(t.TempDir(), "sleep.pid")
	c.SetBuildCmdFunc(func(_ context.Context, _ string) *exec.Cmd {
		return exec.Command("sh", "-c", `sleep 42721 & echo $! > "`+pidFile+`"; wait`)
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, e := c.ExecuteContext(ctx)
		errCh <- e
	}()

	time.Sleep(500 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return after cancellation")
	}

	pidb, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("could not read child pid file: %v", readErr)
	}
	childPid := atoi(t, string(pidb))
	if processStillAlive(childPid, 2*time.Second) {
		t.Fatalf("build child process %d survived cancellation", childPid)
	}
	if got := countWasmCompilerTempFiles(t); got != before {
		t.Fatalf("temp file leaked: before=%d after=%d", before, got)
	}
}

func TestTinygoExecute_NoBuildCmd_ReturnsError(t *testing.T) {
	useWritableTempDir(t)
	var log bytes.Buffer
	c := newTinygoCompiler(t, &log)
	if _, err := c.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected error with no build command")
	}
}

func TestTinygoExecute_BackgroundWrapperMatchesContext(t *testing.T) {
	unixOnly(t)
	useWritableTempDir(t)
	var log bytes.Buffer
	c := newTinygoCompiler(t, &log)
	c.SetBuildCmdFunc(func(_ context.Context, outpath string) *exec.Cmd {
		return exec.Command("sh", "-c", "printf wasm > \"$1\"", "sh", outpath)
	})

	outpath, err := c.Execute()
	if err != nil {
		t.Fatalf("Execute wrapper should still work: %v", err)
	}
	if _, statErr := os.Stat(outpath); statErr != nil {
		t.Fatalf("output missing: %v", statErr)
	}
	os.Remove(outpath)
}
