package platform

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"
)

// Start assigns the suspended process to a kill-on-close job before resuming it.
func Start(cmd *exec.Cmd) (func() error, error) {
	job, e := windows.CreateJobObject(nil, nil)
	if e != nil {
		return nil, e
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, e = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if e != nil {
		windows.CloseHandle(job)
		return nil, e
	}
	var once sync.Once
	var closeErr error
	stop := func() error { once.Do(func() { closeErr = windows.CloseHandle(job) }); return closeErr }
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED, HideWindow: true}
	if e = cmd.Start(); e != nil {
		stop()
		return nil, e
	}
	fail := func(err error) (func() error, error) { cmd.Process.Kill(); cmd.Wait(); stop(); return nil, err }
	p, e := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if e != nil {
		return fail(e)
	}
	e = windows.AssignProcessToJobObject(job, p)
	windows.CloseHandle(p)
	if e != nil {
		return fail(e)
	}
	snap, e := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if e != nil {
		return fail(e)
	}
	defer windows.CloseHandle(snap)
	te := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	e = windows.Thread32First(snap, &te)
	resumed := false
	for e == nil {
		if te.OwnerProcessID == uint32(cmd.Process.Pid) {
			t, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
			if err != nil {
				return fail(err)
			}
			_, err = windows.ResumeThread(t)
			windows.CloseHandle(t)
			if err != nil {
				return fail(err)
			}
			resumed = true
		}
		e = windows.Thread32Next(snap, &te)
	}
	if !resumed {
		return fail(fmt.Errorf("cannot find suspended process thread"))
	}
	return stop, nil
}
