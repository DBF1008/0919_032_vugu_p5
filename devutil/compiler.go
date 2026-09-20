package devutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// NOTE: https://webassembly.org/ says "Wasm" not "WASM" or "WAsm", so that's what I went with on the name.

// NewWasmCompiler returns a WasmCompiler instance.
func NewWasmCompiler() *WasmCompiler {
	return &WasmCompiler{
		logWriter: os.Stderr,
	}
}

// WasmCompiler provides a convenient way to call `go generate` and `go build` and produce Wasm executables for your system.
type WasmCompiler struct {
	beforeFunc      func(ctx context.Context) error
	generateCmdFunc func(ctx context.Context) *exec.Cmd
	buildCmdFunc    func(ctx context.Context, outpath string) *exec.Cmd
	afterFunc       func(ctx context.Context, outpath string, err error) error
	logWriter       io.Writer
}

// SetLogWriter sets the writer to use for logging output.  Setting it to nil disables logging.
// The default from NewWasmCompiler is os.Stderr
func (c *WasmCompiler) SetLogWriter(w io.Writer) *WasmCompiler {
	if w == nil {
		w = io.Discard
	}
	c.logWriter = w
	return c
}

// SetDir sets both the build and generate directories.
func (c *WasmCompiler) SetDir(dir string) *WasmCompiler {
	return c.SetBuildDir(dir).SetGenerateDir(dir)
}

// SetBuildDir sets the directory of the main package, where `go build` will be run.
// Relative paths are okay and will be resolved with filepath.Abs.
func (c *WasmCompiler) SetBuildDir(dir string) *WasmCompiler {
	return c.SetBuildCmdFunc(func(ctx context.Context, outpath string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "go", "build", "-o", outpath)
		cmd.Dir = dir
		cmd.Env = os.Environ()
		cmd.Env = append(cmd.Env, "GOOS=js", "GOARCH=wasm")
		return cmd
	})
}

// SetBuildCmdFunc provides a function to create the exec.Cmd used when running `go build`.
// The provided context is cancelled when Execute is cancelled or times out.
// It overrides any other build-related setting.
func (c *WasmCompiler) SetBuildCmdFunc(cmdf func(ctx context.Context, outpath string) *exec.Cmd) *WasmCompiler {
	c.buildCmdFunc = cmdf
	return c
}

// SetGenerateDir sets the directory of where `go generate` will be run.
// Relative paths are okay and will be resolved with filepath.Abs.
func (c *WasmCompiler) SetGenerateDir(dir string) *WasmCompiler {
	return c.SetGenerateCmdFunc(func(ctx context.Context) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "go", "generate")
		cmd.Dir = dir
		return cmd
	})
}

// SetGenerateCmdFunc provides a function to create the exec.Cmd used when running `go generate`.
// The provided context is cancelled when Execute is cancelled or times out.
// It overrides any other generate-related setting.
func (c *WasmCompiler) SetGenerateCmdFunc(cmdf func(ctx context.Context) *exec.Cmd) *WasmCompiler {
	c.generateCmdFunc = cmdf
	return c
}

// SetBeforeFunc specifies a function to be executed before anything else during Execute().
// The function receives the Execute context and must return promptly once it is cancelled.
func (c *WasmCompiler) SetBeforeFunc(f func(ctx context.Context) error) *WasmCompiler {
	c.beforeFunc = f
	return c
}

// SetAfterFunc specifies a function to be executed after the build completes during Execute().
// It is called even when the build fails, in which case err describes that failure.
// A non-nil error returned by the function is propagated to the caller.
func (c *WasmCompiler) SetAfterFunc(f func(ctx context.Context, outpath string, err error) error) *WasmCompiler {
	c.afterFunc = f
	return c
}

// Execute is a convenience wrapper around ExecuteContext using context.Background.
func (c *WasmCompiler) Execute() (outpath string, err error) {
	return c.ExecuteContext(context.Background())
}

// ExecuteContext runs the generate command (if any) and then invokes the Go
// compiler and produces a wasm executable (or an error).
//
// Cancelling ctx or letting its deadline expire interrupts the running
// generate/build processes and cleans up any temporary output file.
//
// The value of outpath is the absolute path to the output file on disk.
// It will be created with a temporary name and if no error is returned
// it is the caller's responsibility to delete the file when it is no longer needed.
// On any error the temporary file is removed by ExecuteContext, so callers must
// not try to open or remove outpath when err is non-nil.
//
// If an error occurs during any of the steps it will be returned with (possibly multi-line)
// descriptive output in its error message, as produced by the underlying tool.
func (c *WasmCompiler) ExecuteContext(ctx context.Context) (outpath string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}

	logerr := func(e error) error {
		if e == nil {
			return nil
		}
		fmt.Fprintln(c.logWriter, e)
		return e
	}

	if c.buildCmdFunc == nil {
		return "", logerr(errors.New("WasmCompiler: no build command set, cannot continue (did you forget to call SetBuildDir?)"))
	}

	if err := ctx.Err(); err != nil {
		return "", logerr(fmt.Errorf("WasmCompiler: cancelled before build: %w", err))
	}

	if c.beforeFunc != nil {
		if e := c.beforeFunc(ctx); e != nil {
			return "", logerr(fmt.Errorf("WasmCompiler: before func error: %w", e))
		}
	}

	runner := cmdRunner{logWriter: c.logWriter}

	if c.generateCmdFunc != nil {
		if e := runner.runCmd(ctx, "WasmCompiler: generate", c.generateCmdFunc(ctx)); e != nil {
			return "", logerr(e)
		}
		fmt.Fprintln(c.logWriter, "WasmCompiler: Successful generate")
	}

	tmpf, e := os.CreateTemp("", "WasmCompiler")
	if e != nil {
		return "", logerr(fmt.Errorf("WasmCompiler: error creating temporary file: %w", e))
	}
	tmpPath := tmpf.Name()
	if e := tmpf.Close(); e != nil {
		os.Remove(tmpPath)
		return "", logerr(fmt.Errorf("WasmCompiler: error closing temporary file: %w", e))
	}

	// Remove the temporary output on every failure. tmpPath is captured in
	// the closure on purpose: on error the named outpath return is reset to
	// "" before deferred calls run, which would otherwise leave the file on
	// disk. A successful build keeps the file for the caller to consume and
	// eventually delete.
	buildSucceeded := false
	defer func() {
		if !buildSucceeded {
			os.Remove(tmpPath)
		}
	}()

	var buildErr error
	if e := runner.runCmd(ctx, "WasmCompiler: build", c.buildCmdFunc(ctx, tmpPath)); e != nil {
		buildErr = e
	}
	if buildErr == nil {
		fmt.Fprintln(c.logWriter, "WasmCompiler: Successful build")
	}

	if c.afterFunc != nil {
		if e := c.afterFunc(ctx, tmpPath, buildErr); e != nil {
			// The after hook failure takes precedence, but keep the build
			// error around when both failed.
			buildErr = errors.Join(buildErr, fmt.Errorf("WasmCompiler: after func error: %w", e))
		}
	}

	if buildErr != nil {
		return "", logerr(buildErr)
	}

	buildSucceeded = true
	return tmpPath, nil
}

// WasmExecJS returns the contents of the wasm_exec.js file bundled with the Go compiler.
func (c *WasmCompiler) WasmExecJS() (r io.Reader, err error) {

	b1, err := exec.Command("go", "env", "GOROOT").CombinedOutput()
	if err != nil {
		return nil, err
	}

	b2, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(b1)), "misc/wasm/wasm_exec.js"))
	return bytes.NewReader(b2), err

}
