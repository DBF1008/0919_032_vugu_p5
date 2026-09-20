package devutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// tmpFilePrefix is used as the prefix for the wasm output temp files
// created by the compilers.
const tmpFilePrefix = "WasmCompiler"

// processWaitDelay is how long a canceled command is given to release pipes
// and exit after its process group has been killed before the runtime
// reaps it. It prevents leaked descriptors / zombie processes when a build
// is interrupted.
const processWaitDelay = 15 * time.Second

// runCmdStreaming executes cmd honoring ctx cancellation. The command's
// stdout and stderr are teed: every byte is copied to logWriter in real time
// (so long-running builds show progress instead of looking frozen) while the
// combined output is also captured and included in the returned error.
//
// When ctx is canceled (timeout or explicit cancel) the command's entire
// process group is killed, so child processes spawned by e.g. `go build`
// (compile toolchain, `go generate` scripts) are terminated too instead of
// being orphaned in the background.
func runCmdStreaming(ctx context.Context, label string, cmd *exec.Cmd, logWriter io.Writer) error {
	if logWriter == nil {
		logWriter = io.Discard
	}

	var output lockedMultiWriter
	output.add(logWriter)

	// Start in a new process group (Unix) / process group object (Windows)
	// so we can kill the whole tree on cancel.
	configureProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("%s: error creating stdout pipe: %w", label, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("%s: error creating stderr pipe: %w", label, err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: error starting command: %w", label, err)
	}

	// Watch the context ourselves (rather than relying on exec.CommandContext)
	// because the caller-provided command was not created with a context.
	watchDone := make(chan struct{})
	stopWatcher := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			killProcessGroup(cmd)
		case <-stopWatcher:
		}
		close(watchDone)
	}()
	// The runtime will unblock Wait if the process is still alive after this.
	cmd.WaitDelay = processWaitDelay

	var copyWG sync.WaitGroup
	copyWG.Add(2)
	go func() {
		defer copyWG.Done()
		io.Copy(&output, stdout)
	}()
	go func() {
		defer copyWG.Done()
		io.Copy(&output, stderr)
	}()

	waitErr := cmd.Wait()
	copyWG.Wait()
	close(stopWatcher)
	<-watchDone

	if waitErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%s: canceled: %w: %v; output so far:\n%s",
				label, ctxErr, waitErr, output.Bytes())
		}
		return fmt.Errorf("%s: %w; full output:\n%s", label, waitErr, output.Bytes())
	}
	return nil
}

// lockedMultiWriter is a locked fan-out writer: it duplicates writes to
// several writers and buffers everything for later inclusion in errors.
type lockedMultiWriter struct {
	mu      sync.Mutex
	writers []io.Writer
	buf     []byte
}

func (lw *lockedMultiWriter) add(w io.Writer) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	lw.writers = append(lw.writers, w)
}

func (lw *lockedMultiWriter) Write(p []byte) (int, error) {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	lw.buf = append(lw.buf, p...)
	for _, w := range lw.writers {
		w.Write(p)
	}
	return len(p), nil
}

func (lw *lockedMultiWriter) Bytes() []byte {
	lw.mu.Lock()
	defer lw.mu.Unlock()
	return append([]byte(nil), lw.buf...)
}

// compilerRunConfig holds the shared inputs of WasmCompiler.ExecuteContext
// and TinygoCompiler.ExecuteContext.
type compilerRunConfig struct {
	name              string // prefix for error messages, e.g. "WasmCompiler"
	beforeFunc        func() error
	generateCmdFunc   func() *exec.Cmd
	buildCmdFunc      func(outpath string) *exec.Cmd
	afterFunc         func(outpath string, err error) error
	logWriter         io.Writer
	removeBeforeBuild bool // tinygo builds need the pre-created empty temp file removed first
}

// executeContext is the shared implementation of the context-aware build flow:
//
//  1. beforeFunc runs first; its error aborts everything.
//  2. generateCmd (if set) runs with streamed output; its error aborts.
//  3. A temp output file is created and is guaranteed to be removed when a
//     non-nil error is returned (no /tmp residue after repeated failures).
//  4. buildCmd runs with streamed output and can be interrupted by ctx.
//  5. afterFunc is always invoked after the build attempt and receives the
//     build error (if any); its error is propagated to the caller, combined
//     with the build error.
func executeContext(ctx context.Context, cfg compilerRunConfig) (outpath string, err error) {
	logWriter := cfg.logWriter
	if logWriter == nil {
		logWriter = io.Discard
	}

	logerr := func(e error) error {
		if e == nil {
			return nil
		}
		fmt.Fprintln(logWriter, e)
		return e
	}

	if err := ctx.Err(); err != nil {
		return "", logerr(fmt.Errorf("%s: %w", cfg.name, err))
	}

	if cfg.buildCmdFunc == nil {
		return "", logerr(fmt.Errorf("%s: no build command set, cannot continue (did you forget to call SetBuildDir?)", cfg.name))
	}

	if cfg.beforeFunc != nil {
		if err := cfg.beforeFunc(); err != nil {
			return "", logerr(fmt.Errorf("%s: before func error: %w", cfg.name, err))
		}
	}

	if cfg.generateCmdFunc != nil {
		fmt.Fprintf(logWriter, "%s: running go generate...\n", cfg.name)
		if err := runCmdStreaming(ctx, cfg.name+": generate error", cfg.generateCmdFunc(), logWriter); err != nil {
			return "", logerr(err)
		}
		fmt.Fprintf(logWriter, "%s: successful generate\n", cfg.name)
	}

	tmpf, err := os.CreateTemp("", tmpFilePrefix)
	if err != nil {
		return "", logerr(fmt.Errorf("%s: error creating temporary file: %w", cfg.name, err))
	}
	outpath = tmpf.Name()
	// Guaranteed cleanup on any error path below; on success the caller
	// owns the file (documented behavior).
	success := false
	defer func(path string) {
		if !success {
			os.Remove(path)
		}
	}(outpath)

	if err := tmpf.Close(); err != nil {
		return "", logerr(fmt.Errorf("%s: error closing temporary file: %w", cfg.name, err))
	}
	if cfg.removeBeforeBuild {
		if err := os.Remove(outpath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", logerr(fmt.Errorf("%s: error removing temporary file: %w", cfg.name, err))
		}
	}

	buildErr := runCmdStreaming(ctx, cfg.name+": build error", cfg.buildCmdFunc(outpath), logWriter)
	if buildErr == nil {
		fmt.Fprintf(logWriter, "%s: successful build\n", cfg.name)
	}

	var afterErr error
	if cfg.afterFunc != nil {
		afterErr = cfg.afterFunc(outpath, buildErr)
	}

	if finalErr := errors.Join(buildErr, afterErr); finalErr != nil {
		if afterErr != nil && buildErr == nil {
			return "", logerr(fmt.Errorf("%s: after func error: %w", cfg.name, afterErr))
		}
		return "", logerr(finalErr)
	}

	success = true
	return outpath, nil
}
