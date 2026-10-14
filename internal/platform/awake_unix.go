//go:build !windows

package platform

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// KeepAwake holds an OS-provided sleep inhibitor for the session lifetime.
func KeepAwake(ctx context.Context) (func() error, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("caffeinate", "-i")
	} else {
		cmd = exec.Command("systemd-inhibit", "--what=sleep", "--mode=block", "--why=Temporary ShellPier session", "sleep", "infinity")
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stop, e := Start(cmd)
	if e != nil {
		return nil, fmt.Errorf("keep awake unavailable: %w", e)
	}
	exited := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(exited) }()
	// Detect missing system services and authorization failures instead of reporting success.
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-exited:
		stop()
		return nil, fmt.Errorf("keep awake inhibitor exited: %v: %s", waitErr, stderr.String())
	case <-ctx.Done():
		stop()
		<-exited
		return nil, ctx.Err()
	case <-timer.C:
	}
	var once sync.Once
	done := make(chan struct{})
	var result error
	cleanup := func() error { once.Do(func() { result = stop(); <-exited; close(done) }); return result }
	go func() {
		select {
		case <-ctx.Done():
			cleanup()
		case <-done:
		}
	}()
	return cleanup, nil
}
