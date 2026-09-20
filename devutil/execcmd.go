package devutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// cmdRunner executes external commands on behalf of the compilers.
// It streams combined stdout/stderr to logWriter in real time while also
// buffering it so it can be attached to the returned error, and it makes
// sure the whole process tree is terminated when the context is cancelled.
type cmdRunner struct {
	logWriter io.Writer
}

// runCmd starts cmd, tees its combined stdout/stderr to the log in real
// time, and waits for it to finish. If ctx is cancelled (or its deadline
// expires) the command and all of its child processes are killed and the
// context error is returned. On failure the returned error is annotated
// with the command's full output.
func (r cmdRunner) runCmd(ctx context.Context, label string, cmd *exec.Cmd) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if cmd == nil {
		return errors.New(label + ": nil command")
	}

	var output bytes.Buffer
	cmd.Stdout = io.MultiWriter(&output, r.logWriter)
	cmd.Stderr = cmd.Stdout

	// Platform specific setup so the whole process tree can be killed
	// (a new process group on Unix, a new process group on Windows).
	configureCommand(cmd)

	fmt.Fprintf(r.logWriter, "%s: running: %s\n", label, strings.Join(cmd.Args, " "))

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: failed to start command: %w", label, err)
	}

	pid := cmd.Process.Pid
	waitDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			// Kill the entire process tree so child processes such as
			// `go build` and the compiler it spawns do not linger.
			killProcessGroup(pid)
		case <-waitDone:
		}
	}()

	waitErr := cmd.Wait()
	close(waitDone)

	out := strings.TrimSpace(output.String())

	if ctxErr := ctx.Err(); ctxErr != nil {
		if out == "" {
			return fmt.Errorf("%s: %w", label, ctxErr)
		}
		return fmt.Errorf("%s: %w; output so far:\n%s", label, ctxErr, out)
	}

	if waitErr != nil {
		fmt.Fprintf(r.logWriter, "%s: failed: %v\n", label, waitErr)
		if out == "" {
			return fmt.Errorf("%s: %w", label, waitErr)
		}
		return fmt.Errorf("%s: %w; full output:\n%s", label, waitErr, out)
	}

	return nil
}
