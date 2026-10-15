package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestManager(t *testing.T, change func(*Options)) *Manager {
	t.Helper()
	o := DefaultOptions()
	o.WorkDir = t.TempDir()
	if runtime.GOOS == "windows" {
		o.Shell = "cmd"
	} else {
		o.Shell = "sh"
	}
	if change != nil {
		change(&o)
	}
	m, err := New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}
func awaitJob(t *testing.T, m *Manager, id string) Job {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		r := m.Do(Request{Action: "status", ID: id})
		if !r.OK {
			t.Fatal(r.Error)
		}
		if r.Job.State != "running" {
			return *r.Job
		}
		if time.Now().After(deadline) {
			t.Fatal("job did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func TestStartExitLogsAndIdempotency(t *testing.T) {
	m := newTestManager(t, nil)
	command := "printf '中文 output'; printf 'error' >&2; exit 7"
	if runtime.GOOS == "windows" {
		command = "echo output & echo error 1>&2 & exit /b 7"
	}
	request := Request{Action: "start", OperationID: "stable-operation", Command: command}
	first := m.Do(request)
	if !first.OK {
		t.Fatal(first.Error)
	}
	second := m.Do(request)
	if !second.OK || second.Job.ID != first.Job.ID {
		t.Fatal("duplicate start created another task")
	}
	conflict := request
	conflict.Command = "different"
	if m.Do(conflict).OK {
		t.Fatal("conflicting operation content accepted")
	}
	result := awaitJob(t, m, first.Job.ID)
	if result.State != "failed" || result.ExitCode == nil || *result.ExitCode != 7 {
		t.Fatal("exit status lost")
	}
	logs := m.Do(Request{Action: "logs", ID: result.ID})
	if !logs.OK || !strings.Contains(string(logs.Stdout.Data), "output") || !strings.Contains(string(logs.Stderr.Data), "error") {
		t.Fatal("stdout or stderr lost")
	}
	again := m.Do(request)
	if !again.OK || again.Job.ID != result.ID {
		t.Fatal("completed task deduplication lost")
	}
	list := m.Do(Request{Action: "list"})
	if !list.OK || len(list.Jobs) != 1 {
		t.Fatal("list count mismatch")
	}
}
func TestCapacityAndCancellation(t *testing.T) {
	m := newTestManager(t, func(o *Options) { o.MaxConcurrent = 1; o.MaxJobs = 1 })
	command := "sleep 120"
	if runtime.GOOS == "windows" {
		command = "ping -n 120 127.0.0.1 >nul"
	}
	request := Request{Action: "start", OperationID: "first", Command: command}
	response := m.Do(request)
	if !response.OK {
		t.Fatal(response.Error)
	}
	request.OperationID = "another"
	if m.Do(request).OK {
		t.Fatal("capacity ignored")
	}
	if !m.Do(Request{Action: "cancel", ID: response.Job.ID}).OK {
		t.Fatal("cancel failed")
	}
	job := awaitJob(t, m, response.Job.ID)
	if job.State != "cancelled" {
		t.Fatal("cancelled state lost")
	}
	if m.Do(request).OK {
		t.Fatal("completed table entry was evicted and lost deduplication")
	}
}
func TestTaskTimeoutAndHostStop(t *testing.T) {
	m := newTestManager(t, nil)
	command := "sleep 120"
	if runtime.GOOS == "windows" {
		command = "ping -n 120 127.0.0.1 >nul"
	}
	response := m.Do(Request{Action: "start", OperationID: "timeout", Command: command, TimeoutMS: 50})
	if !response.OK {
		t.Fatal(response.Error)
	}
	if result := awaitJob(t, m, response.Job.ID); result.State != "timed_out" {
		t.Fatal("timeout state lost")
	}
	readyCommand := "printf '__HOST_STOP_READY__'; " + command
	if runtime.GOOS == "windows" {
		readyCommand = "echo __HOST_STOP_READY__ & " + command
	}
	response = m.Do(Request{Action: "start", OperationID: "host-stop", Command: readyCommand})
	if !response.OK {
		t.Fatal(response.Error)
	}
	readyDeadline := time.Now().Add(5 * time.Second)
	for {
		logs := m.Do(Request{Action: "logs", ID: response.Job.ID})
		if logs.OK && strings.Contains(string(logs.Stdout.Data), "__HOST_STOP_READY__") {
			break
		}
		if time.Now().After(readyDeadline) {
			t.Fatal("host-stop task did not actually start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("host stop did not cancel tasks")
	}
	m.mu.Lock()
	state := m.entries[response.Job.ID].State
	m.mu.Unlock()
	if state != "cancelled" {
		t.Fatal("host stopped without cancelling job")
	}
	if m.Do(Request{Action: "start", OperationID: "after-close", Command: "echo no"}).OK {
		t.Fatal("closed manager accepted work")
	}
}
func TestLogRingCursorAndDrop(t *testing.T) {
	r := ring{limit: 8}
	r.write([]byte("012345"))
	r.write([]byte("6789AB"))
	first, err := r.read(0, 3)
	if err != nil || string(first.Data) != "456" || first.Cursor != 7 || first.Dropped != 4 {
		t.Fatal("ring did not report absolute drop/cursor")
	}
	next, err := r.read(first.Cursor, 8)
	if err != nil || string(next.Data) != "789AB" || next.Cursor != 12 || next.Dropped != 0 {
		t.Fatal("cursor read was not incremental")
	}
	if _, err = r.read(13, 8); err == nil {
		t.Fatal("future log cursor accepted")
	}
	r.write([]byte("abcdefghijkl"))
	if string(r.data) != "efghijkl" || r.start != 16 || r.end != 24 {
		t.Fatal("oversized append was not bounded")
	}
}
func TestProtocolValidation(t *testing.T) {
	m := newTestManager(t, nil)
	for _, payload := range [][]byte{nil, []byte(`{"action":"list","unknown":true}`), []byte(`{"action":"list"}{}`), []byte(strings.Repeat("x", MaxPayload+1))} {
		if _, err := m.Handle(payload); err == nil {
			t.Fatal("invalid bounded JSON accepted")
		}
	}
	for _, request := range []Request{{Action: "start", Command: "echo no"}, {Action: "start", OperationID: "valid", Command: "bad\x00"}, {Action: "start", OperationID: "valid", Command: "echo no", Env: []string{"ENV=one", "ENV=two"}}, {Action: "start", OperationID: "valid", Command: "echo no", TimeoutMS: -1}} {
		if m.Do(request).OK {
			t.Fatal("invalid start accepted")
		}
	}
	payload, err := m.Handle([]byte(`{"action":"list"}`))
	if err != nil {
		t.Fatal(err)
	}
	var response Response
	if json.Unmarshal(payload, &response) != nil || !response.OK {
		t.Fatal("protocol reply invalid")
	}
}
func TestPowerShellUnicode(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native PowerShell")
	}
	m := newTestManager(t, func(o *Options) { o.Shell = "powershell" })
	response := m.Do(Request{Action: "start", OperationID: "unicode", Command: "[Console]::Write('中文任务'); exit 7"})
	if !response.OK {
		t.Fatal(response.Error)
	}
	result := awaitJob(t, m, response.Job.ID)
	if result.ExitCode == nil || *result.ExitCode != 7 {
		t.Fatal("PowerShell exit status mismatch")
	}
	logs := m.Do(Request{Action: "logs", ID: result.ID})
	if string(logs.Stdout.Data) != "中文任务" {
		t.Fatal("PowerShell Unicode output mismatch")
	}
}

func TestJobWorkingDirectoryAndEnvironment(t *testing.T) {
	m := newTestManager(t, nil)
	subdir := filepath.Join(m.options.WorkDir, "目录 with spaces")
	if err := os.Mkdir(subdir, 0700); err != nil {
		t.Fatal(err)
	}
	command := `printf '%s' "$SHELLPIER_JOB_TEST" > artifact`
	if runtime.GOOS == "windows" {
		command = "echo %SHELLPIER_JOB_TEST%>artifact"
	}
	response := m.Do(Request{Action: "start", OperationID: "cwd-env", Command: command, CWD: "目录 with spaces", Env: []string{"SHELLPIER_JOB_TEST=hello space"}})
	if !response.OK {
		t.Fatal(response.Error)
	}
	if result := awaitJob(t, m, response.Job.ID); result.State != "succeeded" {
		t.Fatal("cwd/environment job failed")
	}
	data, err := os.ReadFile(filepath.Join(subdir, "artifact"))
	if err != nil || strings.TrimSpace(string(data)) != "hello space" {
		t.Fatal("cwd or environment not applied")
	}
}

func TestConcurrentOperationIDAndConcurrencyLimit(t *testing.T) {
	m := newTestManager(t, func(o *Options) { o.MaxConcurrent = 1; o.MaxJobs = 8 })
	command := "sleep 120"
	if runtime.GOOS == "windows" {
		command = "ping -n 120 127.0.0.1 >nul"
	}
	request := Request{Action: "start", OperationID: "same-concurrent-id", Command: command}
	results := make(chan Response, 16)
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); results <- m.Do(request) }()
	}
	workers.Wait()
	close(results)
	id := ""
	for response := range results {
		if !response.OK {
			t.Fatal(response.Error)
		}
		if id == "" {
			id = response.Job.ID
		}
		if response.Job.ID != id {
			t.Fatal("concurrent duplicate starts created different jobs")
		}
	}
	request.OperationID = "different-id"
	if response := m.Do(request); response.OK || !strings.Contains(response.Error, "concurrency") {
		t.Fatal("concurrency limit did not reserve the running slot")
	}
	m.Do(Request{Action: "cancel", ID: id})
	awaitJob(t, m, id)
}
func TestActualLogOutputIsBounded(t *testing.T) {
	m := newTestManager(t, func(o *Options) { o.LogBytes = 1024 })
	command := `i=0; while [ "$i" -lt 2048 ]; do printf x; i=$((i+1)); done`
	if runtime.GOOS == "windows" {
		command = "for /l %i in (1,1,2048) do @echo x"
	}
	response := m.Do(Request{Action: "start", OperationID: "bounded-logs", Command: command})
	if !response.OK {
		t.Fatal(response.Error)
	}
	awaitJob(t, m, response.Job.ID)
	logs := m.Do(Request{Action: "logs", ID: response.Job.ID})
	if !logs.OK || len(logs.Stdout.Data) != 1024 || logs.Stdout.Dropped < 1024 || logs.Stdout.Cursor <= 1024 {
		t.Fatal("actual job output did not report a bounded retained suffix")
	}
	next := m.Do(Request{Action: "logs", ID: response.Job.ID, StdoutCursor: logs.Stdout.Cursor, StderrCursor: logs.Stderr.Cursor})
	if !next.OK || len(next.Stdout.Data) != 0 || next.Stdout.Dropped != 0 {
		t.Fatal("end cursor repeated output")
	}
}

func TestListPagesStayBelowPacketBound(t *testing.T) {
	m := newTestManager(t, func(o *Options) { o.MaxJobs = 1024 })
	for i := 0; i < 260; i++ {
		id := fmt.Sprintf("%032x", i)
		m.entries[id] = &entry{Job: Job{ID: id, OperationID: strings.Repeat("o", 128), State: "succeeded", CreatedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()}}
	}
	cursor := ""
	count := 0
	for {
		payload, err := json.Marshal(Request{Action: "list", After: cursor})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := m.Handle(payload)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > MaxPayload {
			t.Fatal("list reply exceeded bounded SSH request-size budget")
		}
		var response Response
		if json.Unmarshal(raw, &response) != nil || !response.OK {
			t.Fatal("list page invalid")
		}
		if len(response.Jobs) > 128 {
			t.Fatal("list page exceeded limit")
		}
		count += len(response.Jobs)
		if response.NextCursor == "" {
			break
		}
		if response.NextCursor <= cursor {
			t.Fatal("list cursor did not advance")
		}
		cursor = response.NextCursor
	}
	if count != 260 {
		t.Fatal("pagination lost jobs")
	}
}

func TestStatusByOperationID(t *testing.T) {
	m := newTestManager(t, nil)
	started := m.Do(Request{Action: "start", OperationID: "lookup-operation", Command: "echo once"})
	if !started.OK {
		t.Fatal(started.Error)
	}
	result := m.Do(Request{Action: "status", OperationID: "lookup-operation"})
	if !result.OK || result.Job == nil || result.Job.ID != started.Job.ID {
		t.Fatal("operation did not identify accepted job")
	}
	for _, request := range []Request{
		{Action: "status", OperationID: "missing"},
		{Action: "status", OperationID: "bad\nID"},
		{Action: "status", ID: started.Job.ID, OperationID: "lookup-operation"},
		{Action: "cancel", OperationID: "lookup-operation"},
		{Action: "logs", OperationID: "lookup-operation"},
		{Action: "list", OperationID: "lookup-operation"},
	} {
		if m.Do(request).OK {
			t.Fatalf("invalid operation selector accepted for %s", request.Action)
		}
	}
	listed := m.Do(Request{Action: "list"})
	if len(listed.Jobs) != 1 {
		t.Fatal("read-only operation lookup created a task")
	}
}
