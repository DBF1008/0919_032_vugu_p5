//go:build !windows

package devutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// helperProcess builds a self-contained command that re-executes the test
// binary with TestHelperProcess. This is the portable (Windows-safe) pattern
// from the os/exec package documentation.
func helperProcess(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	cs := []string{"-test.run=TestHelperProcess", "--"}
	cs = append(cs, args...)
	cmd := exec.Command(os.Args[0], cs...)
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
	return cmd
}

// helperModes is the registry of helper-process behaviors; platform-specific
// test files may register additional modes from init().
var helperModes = map[string]func(args []string){}

func init() {
	helperModes["sleep"] = func(args []string) {
		time.Sleep(30 * time.Second)
	}
	helperModes["stream"] = func(args []string) {
		// two lines far enough apart to prove real-time streaming
		fmt.Println("line-1")
		os.Stdout.Sync()
		time.Sleep(300 * time.Millisecond)
		fmt.Println("line-2")
	}
	helperModes["fail"] = func(args []string) {
		fmt.Fprintln(os.Stdout, "some build progress")
		fmt.Fprintln(os.Stderr, "boom: something failed")
		os.Exit(7)
	}
	helperModes["touch"] = func(args []string) {
		// args: touch <outpath>
		if len(args) < 1 {
			fmt.Fprintln(os.Stderr, "touch: missing path")
			os.Exit(2)
		}
		if err := os.WriteFile(args[0], []byte("wasm-bytes"), 0644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Println("build-ok")
	}
}

// TestHelperProcess is not a real test; it is the entry point of the
// helper processes spawned above.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "helper: no mode given")
		os.Exit(2)
	}
	mode, ok := helperModes[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "helper: unknown mode %q\n", args[0])
		os.Exit(2)
	}
	mode(args[1:])
	os.Exit(0)
}

// countWasmCompilerTmps returns how many WasmCompiler* temp files exist.
func countWasmCompilerTmps(t *testing.T) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), tmpFilePrefix+"*"))
	if err != nil {
		t.Fatal(err)
	}
	return len(matches)
}

func TestWasmCompilerExecuteSuccess(t *testing.T) {
	var log bytes.Buffer
	wc := NewWasmCompiler().SetLogWriter(&log).
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

func TestWasmCompilerExecuteContextCanceled(t *testing.T) {
	wc := NewWasmCompiler().SetLogWriter(&bytes.Buffer{}).
		SetBuildCmdFunc(func(outpath string) *exec.Cmd {
			return helperProcess(t, "sleep")
		})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	before := countWasmCompilerTmps(t)
	_, err := wc.ExecuteContext(ctx)
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled in error chain, got: %v", err)
	}
	if got := countWasmCompilerTmps(t); got != before {
		t.Fatalf("temp file leaked on cancel: before=%d after=%d", before, got)
	}
}

func TestWasmCompilerExecuteContextDeadline(t *testing.T) {
	wc := NewWasmCompiler().SetLogWriter(&bytes.Buffer{}).
		SetBuildCmdFunc(func(outpath string) *exec.Cmd {
			return helperProcess(t, "sleep")
		})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_, err := wc.ExecuteContext(ctx)
	if err == nil {
		t.Fatal("expected deadline error, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got: %v", err)
	}
}

func TestWasmCompilerGenerateCanceled(t *testing.T) {
	wc := NewWasmCompiler().SetLogWriter(&bytes.Buffer{}).
		SetGenerateCmdFunc(func() *exec.Cmd {
			return helperProcess(t, "sleep")
		}).
		SetBuildCmdFunc(func(outpath string) *exec.Cmd {
			return helperProcess(t, "touch", outpath)
		})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()

	_, err := wc.ExecuteContext(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

func TestWasmCompilerTempFileCleanedOnBuildFailure(t *testing.T) {
	wc := NewWasmCompiler().SetLogWriter(&bytes.Buffer{}).
		SetBuildCmdFunc(func(outpath string) *exec.Cmd {
			return helperProcess(t, "fail")
		})

	before := countWasmCompilerTmps(t)
	_, err := wc.Execute()
	if err == nil {
		t.Fatal("expected build error, got nil")
	}
	if !strings.Contains(err.Error(), "boom: something failed") {
		t.Fatalf("error should contain command output, got: %v", err)
	}
	if got := countWasmCompilerTmps(t); got != before {
		t.Fatalf("temp files leaked: before=%d after=%d", before, got)
	}
}

func TestWasmCompilerRepeatedFailuresLeaveNoResidue(t *testing.T) {
	wc := NewWasmCompiler().SetLogWriter(&bytes.Buffer{}).
		SetBuildCmdFunc(func(outpath string) *exec.Cmd {
			return helperProcess(t, "fail")
		})

	before := countWasmCompilerTmps(t)
	for i := 0; i < 5; i++ {
		if _, err := wc.Execute(); err == nil {
			t.Fatalf("iteration %d: expected error", i)
		}
	}
	if got := countWasmCompilerTmps(t); got != before {
		t.Fatalf("temp files accumulated after failures: before=%d after=%d", before, got)
	}
}

func TestWasmCompilerAfterFuncErrorPropagated(t *testing.T) {
	afterErr := errors.New("post-processing failed")
	var sawBuildErr error
	var called bool
	wc := NewWasmCompiler().SetLogWriter(&bytes.Buffer{}).
		SetBuildCmdFunc(func(outpath string) *exec.Cmd {
			return helperProcess(t, "touch", outpath)
		}).
		SetAfterFunc(func(outpath string, buildErr error) error {
			called = true
			sawBuildErr = buildErr
			return afterErr
		})

	before := countWasmCompilerTmps(t)
	outpath, err := wc.Execute()
	if outpath != "" {
		t.Fatalf("outpath must be empty on afterFunc error, got %q", outpath)
	}
	if !errors.Is(err, afterErr) {
		t.Fatalf("expected afterErr, got: %v", err)
	}
	if !called || sawBuildErr != nil {
		t.Fatalf("afterFunc not called with nil build err: called=%v buildErr=%v", called, sawBuildErr)
	}
	if got := countWasmCompilerTmps(t); got != before {
		t.Fatalf("temp file leaked after afterFunc failure: before=%d after=%d", before, got)
	}
}

func TestWasmCompilerAfterFuncReceivesBuildError(t *testing.T) {
	var sawBuildErr error
	wc := NewWasmCompiler().SetLogWriter(&bytes.Buffer{}).
		SetBuildCmdFunc(func(outpath string) *exec.Cmd {
			return helperProcess(t, "fail")
		}).
		SetAfterFunc(func(outpath string, buildErr error) error {
			sawBuildErr = buildErr
			return nil
		})

	_, err := wc.Execute()
	if err == nil {
		t.Fatal("expected build error")
	}
	if sawBuildErr == nil {
		t.Fatal("afterFunc should receive the build error")
	}
}

func TestWasmCompilerBeforeFuncError(t *testing.T) {
	wc := NewWasmCompiler().SetLogWriter(&bytes.Buffer{}).
		SetBeforeFunc(func() error {
			return errors.New("before boom")
		}).
		SetBuildCmdFunc(func(outpath string) *exec.Cmd {
			t.Fatal("build must not run when beforeFunc fails")
			return nil
		})

	_, err := wc.Execute()
	if err == nil || !strings.Contains(err.Error(), "before boom") {
		t.Fatalf("expected before func error, got: %v", err)
	}
}

func TestWasmCompilerNoBuildCmd(t *testing.T) {
	if _, err := NewWasmCompiler().Execute(); err == nil {
		t.Fatal("expected error when no build command is set")
	}
}

func TestWasmCompilerPreCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewWasmCompiler().
		SetBuildCmdFunc(func(outpath string) *exec.Cmd { return helperProcess(t, "sleep") }).
		ExecuteContext(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

// TestWasmCompilerStreamsOutputInRealTime verifies that build output reaches
// the log writer while the process is still running, not only after exit.
func TestWasmCompilerStreamsOutputInRealTime(t *testing.T) {
	var log bytes.Buffer
	firstLine := make(chan struct{})
	lw := &chanWriter{buf: &log, ch: firstLine, prefix: "line-1"}

	wc := NewWasmCompiler().SetLogWriter(lw).
		SetGenerateCmdFunc(func() *exec.Cmd {
			return helperProcess(t, "stream")
		}).
		SetBuildCmdFunc(func(outpath string) *exec.Cmd {
			return helperProcess(t, "touch", outpath)
		})

	done := make(chan error, 1)
	go func() {
		_, err := wc.Execute()
		done <- err
	}()

	select {
	case <-firstLine:
		// streamed before the process exited (it sleeps 300ms between lines)
	case <-time.After(2 * time.Second):
		t.Fatal("first output line was not streamed in real time")
	}
	if err := <-done; err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(log.String(), "line-1") || !strings.Contains(log.String(), "line-2") {
		t.Fatalf("missing streamed output:\n%s", log.String())
	}
}

func TestWasmCompilerNilContextDoesNotPanic(t *testing.T) {
	wc := NewWasmCompiler().SetLogWriter(&bytes.Buffer{}).
		SetBuildCmdFunc(func(outpath string) *exec.Cmd {
			return helperProcess(t, "touch", outpath)
		})
	outpath, err := wc.ExecuteContext(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	os.Remove(outpath)
}

type chanWriter struct {
	buf    *bytes.Buffer
	ch     chan struct{}
	prefix string
	once   sync.Once
}

func (w *chanWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	if bytes.Contains(p, []byte(w.prefix)) {
		w.once.Do(func() { close(w.ch) })
	}
	return len(p), nil
}
