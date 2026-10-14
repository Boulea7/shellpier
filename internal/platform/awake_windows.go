package platform

import (
	"context"
	"fmt"
	"golang.org/x/sys/windows"
	"runtime"
	"sync"
)

func KeepAwake(ctx context.Context) (func() error, error) {
	ready := make(chan error, 1)
	stop := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(done)
		proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")
		r, _, e := proc.Call(0x80000000 | 0x1)
		if r == 0 {
			ready <- fmt.Errorf("SetThreadExecutionState: %w", e)
			return
		}
		ready <- nil
		select {
		case <-ctx.Done():
		case <-stop:
		}
		proc.Call(0x80000000)
	}()
	if e := <-ready; e != nil {
		return nil, e
	}
	return func() error { once.Do(func() { close(stop) }); <-done; return nil }, nil
}
