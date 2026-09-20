package devutil_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/vugu/vugu/devutil"
)

// syncBuffer is an io.Writer safe for concurrent writes and reads, used so
// the test can poll streamed output while a command is still writing.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// countWasmCompilerTempFiles returns how many leftover WasmCompiler temp
// files exist in the system temp directory.
func countWasmCompilerTempFiles(t *testing.T) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "WasmCompiler*"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	return len(matches)
}

// useWritableTempDir points TMPDIR at a per-test writable directory so that
// temporary output files are created somewhere the process is allowed to
// remove them, regardless of the host sandbox configuration.
func useWritableTempDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
}

// processStillAlive polls signal 0 until the process disappears or the
// timeout elapses; it returns true if the process is still alive afterwards.
func processStillAlive(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if err := syscall.Kill(pid, syscall.Signal(0)); err != nil {
			return false
		}
		if time.Now().After(deadline) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	n := 0
	s = strings.TrimSpace(s)
	for _, r := range s {
		if r < '0' || r > '9' {
			t.Fatalf("invalid pid %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n
}

// unixOnly skips the test on platforms where the shell based test commands
// are not available.
func unixOnly(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell based test requires a unix-like environment")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh not found: %v", err)
	}
}

// newGoCompiler returns a compiler whose build command writes the output file.
func newGoCompiler(log *bytes.Buffer) *devutil.WasmCompiler {
	c := devutil.NewWasmCompiler().SetLogWriter(log)
	c.SetBuildCmdFunc(func(_ context.Context, outpath string) *exec.Cmd {
		return exec.Command("sh", "-c", "printf built > \"$1\"", "sh", outpath)
	})
	return c
}

func TestExecute_Success_ReturnsOutpath(t *testing.T) {
	unixOnly(t)
	useWritableTempDir(t)
	var log bytes.Buffer
	c := newGoCompiler(&log)

	outpath, err := c.ExecuteContext(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer os.Remove(outpath)

	if _, err := os.Stat(outpath); err != nil {
		t.Fatalf("expected output file to exist: %v", err)
	}
	if !strings.Contains(log.String(), "Successful build") {
		t.Fatalf("expected successful build log, got:\n%s", log.String())
	}
}

func TestExecute_BuildError_CleansUpTempFile(t *testing.T) {
	unixOnly(t)
	useWritableTempDir(t)
	before := countWasmCompilerTempFiles(t)

	var log bytes.Buffer
	c := devutil.NewWasmCompiler().SetLogWriter(&log)
	c.SetBuildCmdFunc(func(_ context.Context, _ string) *exec.Cmd {
		return exec.Command("sh", "-c", "echo boom-build-output; exit 7")
	})

	outpath, err := c.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected build error, got nil")
	}
	if outpath != "" {
		t.Fatalf("expected empty outpath on error, got %q", outpath)
	}
	if !strings.Contains(err.Error(), "boom-build-output") {
		t.Fatalf("error should contain command output, got:\n%v", err)
	}
	if got := countWasmCompilerTempFiles(t); got != before {
		t.Fatalf("temp file leaked: before=%d after=%d", before, got)
	}
}

func TestExecute_GenerateError_CleansUpTempFile(t *testing.T) {
	unixOnly(t)
	useWritableTempDir(t)
	before := countWasmCompilerTempFiles(t)

	var log bytes.Buffer
	c := newGoCompiler(&log)
	c.SetGenerateCmdFunc(func(_ context.Context) *exec.Cmd {
		return exec.Command("sh", "-c", "echo boom-generate; exit 3")
	})

	if _, err := c.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected generate error, got nil")
	} else if !strings.Contains(err.Error(), "boom-generate") {
		t.Fatalf("error should contain generate output, got:\n%v", err)
	}

	if got := countWasmCompilerTempFiles(t); got != before {
		t.Fatalf("temp file leaked after generate failure: before=%d after=%d", before, got)
	}
}

func TestExecute_BeforeError_Propagated(t *testing.T) {
	unixOnly(t)
	useWritableTempDir(t)
	var log bytes.Buffer
	c := newGoCompiler(&log)
	c.SetBeforeFunc(func(context.Context) error {
		return errors.New("before exploded")
	})

	outpath, err := c.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "before exploded") {
		t.Fatalf("expected before func error, got %v", err)
	}
	if outpath != "" {
		t.Fatalf("expected empty outpath, got %q", outpath)
	}
}

func TestExecute_AfterError_PropagatedAndTempCleaned(t *testing.T) {
	unixOnly(t)
	useWritableTempDir(t)
	before := countWasmCompilerTempFiles(t)

	var log bytes.Buffer
	c := newGoCompiler(&log)
	var afterCalledWithError error
	var afterSawFile bool
	c.SetAfterFunc(func(_ context.Context, outpath string, buildErr error) error {
		afterCalledWithError = buildErr
		if _, err := os.Stat(outpath); err == nil {
			afterSawFile = true
		}
		return errors.New("after failed")
	})

	outpath, err := c.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "after failed") {
		t.Fatalf("expected after func error to be propagated, got %v", err)
	}
	if outpath != "" {
		t.Fatalf("expected empty outpath when after failed, got %q", outpath)
	}
	if afterCalledWithError != nil {
		t.Fatalf("expected nil build error passed to after func, got %v", afterCalledWithError)
	}
	if !afterSawFile {
		t.Fatal("after func should see a built output file")
	}
	if got := countWasmCompilerTempFiles(t); got != before {
		t.Fatalf("temp file leaked after after-func failure: before=%d after=%d", before, got)
	}
}

func TestExecute_AfterReceivesBuildError(t *testing.T) {
	unixOnly(t)
	useWritableTempDir(t)
	var log bytes.Buffer
	c := devutil.NewWasmCompiler().SetLogWriter(&log)
	c.SetBuildCmdFunc(func(_ context.Context, _ string) *exec.Cmd {
		return exec.Command("sh", "-c", "exit 9")
	})
	var seenErr error
	c.SetAfterFunc(func(_ context.Context, _ string, buildErr error) error {
		seenErr = buildErr
		return nil
	})

	if _, err := c.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected build error")
	}
	if seenErr == nil {
		t.Fatal("after func should have received the build error")
	}
}

func TestExecute_NoBuildCmd_ReturnsError(t *testing.T) {
	useWritableTempDir(t)
	var log bytes.Buffer
	c := devutil.NewWasmCompiler().SetLogWriter(&log)
	if _, err := c.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected error with no build command")
	}
}

func TestExecute_PreCancelledContext_ReturnsCancellation(t *testing.T) {
	unixOnly(t)
	useWritableTempDir(t)
	var log bytes.Buffer
	c := newGoCompiler(&log)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := c.ExecuteContext(ctx)
	if time.Since(start) > 2*time.Second {
		t.Fatal("pre-cancelled context should return immediately")
	}
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled in error chain, got %v", err)
	}
}

func TestExecute_CancelKillsBuildProcess(t *testing.T) {
	unixOnly(t)
	useWritableTempDir(t)
	before := countWasmCompilerTempFiles(t)

	var log bytes.Buffer
	c := devutil.NewWasmCompiler().SetLogWriter(&log)
	pidFile := filepath.Join(t.TempDir(), "sleep.pid")
	c.SetBuildCmdFunc(func(_ context.Context, _ string) *exec.Cmd {
		// Record the child sleep's pid so we can prove the whole tree dies.
		return exec.Command("sh", "-c", `sleep 42719 & echo $! > "`+pidFile+`"; wait`)
	})

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, e := c.ExecuteContext(ctx)
		errCh <- e
	}()

	// Give the command time to start.
	time.Sleep(500 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected cancellation error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return after cancellation")
	}

	// The child sleep process (and the sh parent) must be gone.
	pidb, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("could not read child pid file: %v", readErr)
	}
	childPid := atoi(t, string(pidb))
	if processStillAlive(childPid, 2*time.Second) {
		t.Fatalf("build child process %d survived cancellation", childPid)
	}

	if got := countWasmCompilerTempFiles(t); got != before {
		t.Fatalf("temp file leaked after cancellation: before=%d after=%d", before, got)
	}
}

func TestExecute_DeadlineInterruptsGenerate(t *testing.T) {
	unixOnly(t)
	useWritableTempDir(t)
	var log bytes.Buffer
	c := newGoCompiler(&log)
	pidFile := filepath.Join(t.TempDir(), "sleep.pid")
	c.SetGenerateCmdFunc(func(_ context.Context) *exec.Cmd {
		return exec.Command("sh", "-c", `sleep 42720 & echo $! > "`+pidFile+`"; wait`)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()

	_, err := c.ExecuteContext(ctx)
	if err == nil {
		t.Fatal("expected deadline error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}

	pidb, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatalf("could not read child pid file: %v", readErr)
	}
	childPid := atoi(t, string(pidb))
	if processStillAlive(childPid, 2*time.Second) {
		t.Fatalf("generate child process %d survived deadline", childPid)
	}
}

func TestExecute_StreamsOutputInRealTime(t *testing.T) {
	unixOnly(t)
	useWritableTempDir(t)
	var log syncBuffer
	c := devutil.NewWasmCompiler().SetLogWriter(&log)
	c.SetGenerateCmdFunc(func(_ context.Context) *exec.Cmd {
		// Emit a line and flush it; we assert the line reaches the log
		// writer while the command is still running by polling.
		return exec.Command("sh", "-c", "echo live-line-one; sleep 1; echo live-line-two")
	})
	c.SetBuildCmdFunc(func(_ context.Context, outpath string) *exec.Cmd {
		return exec.Command("sh", "-c", "printf built > \"$1\"", "sh", outpath)
	})

	done := make(chan struct{})
	go func() {
		_, _ = c.ExecuteContext(context.Background())
		close(done)
	}()

	sawLine := false
	deadline := time.Now().Add(800 * time.Millisecond)
	for time.Now().Before(deadline) {
		if strings.Contains(log.String(), "live-line-one") {
			sawLine = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !sawLine {
		t.Fatal("first generate line was not streamed before the command finished")
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("execute did not finish")
	}
	if !strings.Contains(log.String(), "live-line-two") {
		t.Fatalf("expected both lines in log, got:\n%s", log.String())
	}
}

func TestExecute_ContextPassedToHooksAndCmds(t *testing.T) {
	unixOnly(t)
	useWritableTempDir(t)
	type ctxKey struct{}
	var sawBefore, sawGenerate, sawBuild, sawAfter bool

	c := devutil.NewWasmCompiler().SetLogWriter(&bytes.Buffer{})
	c.SetBeforeFunc(func(ctx context.Context) error {
		if ctx.Value(ctxKey{}) != "marker" {
			t.Fatal("before func did not receive the request context")
		}
		sawBefore = true
		return nil
	})
	c.SetGenerateCmdFunc(func(ctx context.Context) *exec.Cmd {
		if ctx.Value(ctxKey{}) != "marker" {
			t.Fatal("generate cmd func did not receive the request context")
		}
		sawGenerate = true
		return exec.Command("true")
	})
	c.SetBuildCmdFunc(func(ctx context.Context, outpath string) *exec.Cmd {
		if ctx.Value(ctxKey{}) != "marker" {
			t.Fatal("build cmd func did not receive the request context")
		}
		sawBuild = true
		return exec.Command("sh", "-c", "printf built > \"$1\"", "sh", outpath)
	})
	c.SetAfterFunc(func(ctx context.Context, _ string, _ error) error {
		if ctx.Value(ctxKey{}) != "marker" {
			t.Fatal("after func did not receive the request context")
		}
		sawAfter = true
		return nil
	})

	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
	outpath, err := c.ExecuteContext(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	os.Remove(outpath)

	if !(sawBefore && sawGenerate && sawBuild && sawAfter) {
		t.Fatalf("not all hooks ran: before=%v generate=%v build=%v after=%v",
			sawBefore, sawGenerate, sawBuild, sawAfter)
	}
}
