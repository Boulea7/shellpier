package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	jobstate "github.com/Boulea7/shellpier/internal/jobs"
	"golang.org/x/crypto/ssh"
)

func TestHostFeatureJobChild(t *testing.T) {
	if os.Getenv("SHELLPIER_HOST_JOB_HELPER") != "1" {
		t.Skip("isolated child helper")
	}
	if err := os.WriteFile(os.Getenv("SHELLPIER_HOST_JOB_PID"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "__HOST_JOB_READY__")
	time.Sleep(120 * time.Second)
}
func hostFeatureDial(t *testing.T, ticketPath string) *ssh.Client {
	t.Helper()
	ticket, err := access.Read("@" + ticketPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	client, err := access.Dial(ctx, ticket, "")
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close(); cancel() })
	return client
}
func hostFeatureCaps(t *testing.T, client *ssh.Client) map[string]any {
	t.Helper()
	ok, data, err := client.SendRequest("capabilities@shellpier", true, nil)
	if err != nil || !ok {
		t.Fatal("capabilities request failed")
	}
	var values map[string]any
	if json.Unmarshal(data, &values) != nil {
		t.Fatal("invalid capabilities JSON")
	}
	return values
}
func TestHostFeaturesDefaultDisabledAndOptIn(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		conn, err := target.Accept()
		if err == nil {
			defer conn.Close()
			io.Copy(conn, conn)
		}
	}()
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			ticket := filepath.Join(t.TempDir(), "access.json")
			args := []string{"share", "--listen", "127.0.0.1:0", "--ttl", "0", "--output", ticket, "--json"}
			if enabled {
				args = append(args, "--allow-jobs", "--allow-forward", target.Addr().String())
			}
			state, done := startHost(t, args...)
			client := hostFeatureDial(t, ticket)
			caps := hostFeatureCaps(t, client)
			if caps["jobs"] != enabled || caps["forward"] != enabled {
				t.Fatal("host capabilities did not reflect explicit opt-in")
			}
			ok, _, err := client.SendRequest("jobs@shellpier", true, []byte(`{"action":"list"}`))
			if err != nil || ok != enabled {
				t.Fatal("jobs permission did not match host opt-in")
			}
			forwarded, err := client.Dial("tcp", target.Addr().String())
			if !enabled {
				if err == nil {
					forwarded.Close()
					t.Fatal("forwarding worked without opt-in")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				forwarded.SetDeadline(time.Now().Add(time.Second))
				if _, err = forwarded.Write([]byte("echo")); err != nil {
					t.Fatal(err)
				}
				result := make([]byte, 4)
				if _, err = io.ReadFull(forwarded, result); err != nil || string(result) != "echo" {
					t.Fatal("allowed forwarding did not carry data")
				}
				forwarded.Close()
			}
			invoke(t, "stop", "--state", state)
			awaitHost(t, done, state)
		})
	}
}
func hostFeatureJobProcess(t *testing.T, client *ssh.Client, dir string) *os.Process {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := "'" + strings.ReplaceAll(exe, "'", "'\\''") + "'"
	if runtime.GOOS == "windows" {
		quote = "\"" + exe + "\""
	}
	pidFile := filepath.Join(dir, "owned.pid")
	request := jobstate.Request{Action: "start", OperationID: "host-lifecycle", Command: quote + " -test.run=^TestHostFeatureJobChild$", Env: []string{"SHELLPIER_HOST_JOB_HELPER=1", "SHELLPIER_HOST_JOB_PID=" + pidFile}}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	ok, data, err := client.SendRequest("jobs@shellpier", true, payload)
	if err != nil || !ok {
		t.Fatal("enabled host rejected job start")
	}
	var response jobstate.Response
	if json.Unmarshal(data, &response) != nil || !response.OK {
		t.Fatal("host job start failed")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, err := os.ReadFile(pidFile); err == nil {
			pid, err := strconv.Atoi(string(data))
			if err == nil && pid > 0 {
				process, err := os.FindProcess(pid)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { process.Release() })
				return process
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("host job child did not actually start")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func assertHostFeatureProcessExited(t *testing.T, process *os.Process) {
	t.Helper()
	if runtime.GOOS == "windows" {
		done := make(chan error, 1)
		go func() { _, err := process.Wait(); done <- err }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("could not observe owned Windows process exit: %v", err)
			}
			return
		case <-time.After(time.Second):
			t.Fatal("host stop left its owned job child running")
		}
	}
	deadline := time.Now().Add(time.Second)
	for {
		err := process.Signal(syscall.Signal(0))
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatalf("could not observe owned process exit: %v", err)
		}
		if runtime.GOOS == "linux" {
			if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", process.Pid)); err == nil {
				end := strings.LastIndexByte(string(data), ')')
				if end >= 0 {
					fields := strings.Fields(string(data[end+1:]))
					if len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X") {
						return
					}
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("host stop left its owned job child running")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func TestHostFeaturesJobsStopAndTTL(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	for _, ttl := range []string{"0", "2s"} {
		t.Run(ttl, func(t *testing.T) {
			dir := t.TempDir()
			ticket := filepath.Join(dir, "access.json")
			shell := "sh"
			if runtime.GOOS == "windows" {
				shell = "cmd"
			}
			state, done := startHost(t, "share", "--listen", "127.0.0.1:0", "--ttl", ttl, "--allow-jobs", "--shell", shell, "--output", ticket, "--json")
			client := hostFeatureDial(t, ticket)
			process := hostFeatureJobProcess(t, client, dir)
			client.Close()
			// A fresh authenticated connection must see the job owned by the host.
			other := hostFeatureDial(t, ticket)
			ok, data, err := other.SendRequest("jobs@shellpier", true, []byte(`{"action":"list"}`))
			var response jobstate.Response
			if err != nil || !ok || json.Unmarshal(data, &response) != nil || len(response.Jobs) != 1 {
				t.Fatal("job did not survive connection replacement")
			}
			other.Close()
			if ttl == "0" {
				invoke(t, "stop", "--state", state)
			}
			awaitHost(t, done, state)
			assertHostFeatureProcessExited(t, process)
		})
	}
}
func TestHostFeatureLimitsRejectedBeforeNetworking(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	t.Setenv("SHELLPIER_RELAY_KEY", strings.Repeat("k", 64))
	var requests atomic.Int32
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "must not be reached", 500)
	}))
	defer relay.Close()
	invalid := [][]string{{"--max-jobs", "0"}, {"--max-jobs", "1025"}, {"--max-running-jobs", "65"}, {"--max-jobs", "1", "--max-running-jobs", "2"}, {"--job-log-limit", "1023"}, {"--job-log-limit", "16777217"}, {"--allow-forward", "*:*"}}
	for _, arguments := range invalid {
		for _, mode := range []string{"direct", "relay"} {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			var out bytes.Buffer
			args := []string{"share"}
			if mode == "direct" {
				args = append(args, "--listen", "invalid-listener-address")
			} else {
				args = append(args, "--relay", relay.URL)
			}
			args = append(args, arguments...)
			err := Run(ctx, args, nil, &out, io.Discard, "test")
			cancel()
			if err == nil {
				t.Fatal("invalid host feature limits accepted")
			}
			if mode == "direct" && strings.Contains(err.Error(), "missing port") {
				t.Fatal("listener was attempted before host feature validation")
			}
			if out.Len() != 0 {
				t.Fatal("invalid host limits emitted session metadata")
			}
		}
	}
	if requests.Load() != 0 {
		t.Fatal("relay was contacted before feature limits were validated")
	}
}
