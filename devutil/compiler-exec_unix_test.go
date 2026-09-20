//go:build !windows

package devutil

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// fork-sleep mode: fork a grandchild that sleeps forever, then sleep
// ourselves. If only the parent is killed, the grandchild leaks.
func init() {
	helperModes["fork-sleep"] = func(args []string) {
		child := exec.Command(os.Args[0], "-test.run=TestHelperProcess", "--", "sleep")
		child.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		child.Stdout = nil
		child.Stderr = nil
		if err := child.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Println(child.Process.Pid)
		os.Stdout.Sync()
		time.Sleep(30 * time.Second)
	}
}

func processExists(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func TestRunCmdStreamingKillsProcessTreeOnCancel(t *testing.T) {
	cmd := helperProcess(t, "fork-sleep")
	pidCh := make(chan int, 1)
	lw := &pidLineWriter{ch: pidCh}
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- runCmdStreaming(ctx, "test", cmd, lw)
	}()

	// wait until the grandchild pid is printed
	var childPID int
	select {
	case childPID = <-pidCh:
	case <-time.After(5 * time.Second):
		t.Fatal("did not receive grandchild pid")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runCmdStreaming did not return after cancel")
	}

	// Give the kernel a moment to deliver SIGKILL to the group.
	deadline := time.Now().Add(3 * time.Second)
	for processExists(childPID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if processExists(childPID) {
		syscall.Kill(-childPID, syscall.SIGKILL)
		t.Fatalf("grandchild process %d survived cancellation: process group was not killed", childPID)
	}
}

// pidLineWriter forwards the first newline-delimited token to ch if it
// parses as a pid; all other bytes are discarded.
type pidLineWriter struct {
	ch  chan int
	buf []byte
}

func (w *pidLineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := bytes.TrimSpace(w.buf[:i])
		w.buf = w.buf[i+1:]
		if pid, err := strconv.Atoi(string(line)); err == nil {
			select {
			case w.ch <- pid:
			default:
			}
		}
	}
	return len(p), nil
}
