package devutil

import (
	"bytes"
	"context"
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
	beforeFunc      func() error
	generateCmdFunc func() *exec.Cmd
	buildCmdFunc    func(outpath string) *exec.Cmd
	afterFunc       func(outpath string, err error) error
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
	return c.SetBuildCmdFunc(func(outpath string) *exec.Cmd {
		cmd := exec.Command("go", "build", "-o", outpath)
		cmd.Dir = dir
		cmd.Env = os.Environ()
		cmd.Env = append(cmd.Env, "GOOS=js", "GOARCH=wasm")
		return cmd
	})
}

// SetBuildCmdFunc provides a function to create the exec.Cmd used when running `go build`.
// It overrides any other build-related setting.
func (c *WasmCompiler) SetBuildCmdFunc(cmdf func(outpath string) *exec.Cmd) *WasmCompiler {
	c.buildCmdFunc = cmdf
	return c
}

// SetGenerateDir sets the directory of where `go generate` will be run.
// Relative paths are okay and will be resolved with filepath.Abs.
func (c *WasmCompiler) SetGenerateDir(dir string) *WasmCompiler {
	return c.SetGenerateCmdFunc(func() *exec.Cmd {
		cmd := exec.Command("go", "generate")
		cmd.Dir = dir
		return cmd
	})
}

// SetGenerateCmdFunc provides a function to create the exec.Cmd used when running `go generate`.
// It overrides any other generate-related setting.
func (c *WasmCompiler) SetGenerateCmdFunc(cmdf func() *exec.Cmd) *WasmCompiler {
	c.generateCmdFunc = cmdf
	return c
}

// SetBeforeFunc specifies a function to be executed before anything else during Execute().
func (c *WasmCompiler) SetBeforeFunc(f func() error) *WasmCompiler {
	c.beforeFunc = f
	return c
}

// SetAfterFunc specifies a function to be executed after everthing else during Execute().
func (c *WasmCompiler) SetAfterFunc(f func(outpath string, err error) error) *WasmCompiler {
	c.afterFunc = f
	return c
}

// Execute runs the generate command (if any) and then invokes the Go compiler
// and produces a wasm executable (or an error).
// It is a shortcut for ExecuteContext with context.Background(); prefer
// ExecuteContext when a timeout or cancellation signal is available.
func (c *WasmCompiler) Execute() (outpath string, err error) {
	return c.ExecuteContext(context.Background())
}

// ExecuteContext is like Execute but honors ctx: when ctx is canceled or
// times out the running generate/build process (and its children) is killed
// and ctx's error is returned.
// The value of outpath is the absolute path to the output file on disk.
// It will be created with a temporary name and if no error is returned
// it is the caller's responsibility to delete the file when it is no longer
// needed. On any error the temporary file is removed automatically.
// Generate/build output is streamed to the configured log writer in real
// time and, on failure, also included in the returned error.
func (c *WasmCompiler) ExecuteContext(ctx context.Context) (outpath string, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return executeContext(ctx, compilerRunConfig{
		name:            "WasmCompiler",
		beforeFunc:      c.beforeFunc,
		generateCmdFunc: c.generateCmdFunc,
		buildCmdFunc:    c.buildCmdFunc,
		afterFunc:       c.afterFunc,
		logWriter:       c.logWriter,
	})
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
