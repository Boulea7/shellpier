package access

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestConfigOptionsValidation(t *testing.T) {
	defaults := DefaultSSHConfigOptions()
	cases := []struct {
		name      string
		options   SSHConfigOptions
		dir, goos string
	}{
		{"fractional interval", SSHConfigOptions{ServerAliveInterval: time.Millisecond}, "/tmp/a", "linux"},
		{"negative interval", SSHConfigOptions{ServerAliveInterval: -time.Second}, "/tmp/a", "linux"},
		{"interval overflow", SSHConfigOptions{ServerAliveInterval: 2147483648 * time.Second}, "/tmp/a", "linux"},
		{"negative count", SSHConfigOptions{ServerAliveCountMax: -1}, "/tmp/a", "linux"},
		{"fractional persist", SSHConfigOptions{ControlPersist: time.Millisecond}, "/tmp/a", "linux"},
		{"large persist", SSHConfigOptions{ControlPersist: time.Hour + time.Second}, "/tmp/a", "linux"},
		{"negative persist", SSHConfigOptions{ControlPersist: -time.Second}, "/tmp/a", "linux"},
		{"windows persist", SSHConfigOptions{ControlPersist: time.Second}, "/tmp/a", "windows"},
		{"long socket", SSHConfigOptions{ControlPersist: time.Second}, "/tmp/" + strings.Repeat("a", 80), "linux"},
		{"control character", SSHConfigOptions{ControlPersist: time.Second}, "/tmp/a\tb", "linux"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := configOptions(tc.options, tc.dir, tc.goos); err == nil {
				t.Fatal("invalid options accepted")
			}
		})
	}
	if _, err := configOptions(defaults, "/tmp/a", "windows"); err != nil {
		t.Fatal(err)
	}
	disabled, err := configOptions(SSHConfigOptions{}, "/tmp/a", "linux")
	if err != nil || !strings.Contains(disabled, "ServerAliveInterval 0\n") || strings.Contains(disabled, "ControlMaster") {
		t.Fatalf("disabled options: %q, %v", disabled, err)
	}
}

func TestConfigOptionsRejectBeforeCreation(t *testing.T) {
	ticket := directTicket(t)
	for _, options := range []SSHConfigOptions{{ServerAliveInterval: time.Millisecond}, {ControlPersist: time.Hour + time.Second}} {
		dir := filepath.Join(t.TempDir(), "new")
		if _, err := WriteConfigWithOptions(ticket, dir, "/usr/bin/env", options); err == nil {
			t.Fatal("invalid options accepted")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("output created: %v", err)
		}
	}
	if runtime.GOOS == "windows" {
		dir := filepath.Join(t.TempDir(), "multiplex")
		if _, err := WriteConfigWithOptions(ticket, dir, "shellpier.exe", SSHConfigOptions{ControlPersist: time.Second}); err == nil {
			t.Fatal("Windows multiplexing accepted")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("output created: %v", err)
		}
	}
	if runtime.GOOS != "windows" {
		for _, name := range []string{strings.Repeat("a", 90), "bad\tpath"} {
			dir := filepath.Join(t.TempDir(), name)
			if _, err := WriteConfigWithOptions(ticket, dir, "/usr/bin/env", SSHConfigOptions{ControlPersist: time.Second}); err == nil {
				t.Fatal("long socket accepted")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("output created: %v", err)
			}
		}
	}
}

func TestConfigOptionsOpenSSH(t *testing.T) {
	ssh, err := exec.LookPath("ssh")
	if err != nil {
		t.Skip("OpenSSH unavailable")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Unix multiplexing configuration")
	}
	// Keep the socket path within portable sockaddr_un limits, including on macOS.
	base, err := os.MkdirTemp("/tmp", "sp-config-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	var controls []string
	for _, name := range []string{"one space %", "two"} {
		ticket := directTicket(t)
		options := DefaultSSHConfigOptions()
		options.ControlPersist = time.Minute
		path, err := WriteConfigWithOptions(ticket, filepath.Join(base, name), "/usr/bin/env", options)
		if err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(ssh, "-G", "-F", path, "shellpier").CombinedOutput()
		if err != nil {
			t.Fatalf("ssh -G: %v: %s", err, out)
		}
		for _, want := range []string{"serveraliveinterval 15\n", "serveralivecountmax 3\n", "controlmaster auto\n", "controlpersist 60\n", "stricthostkeychecking true\n", "batchmode yes\n"} {
			if !strings.Contains(string(out), want) {
				t.Fatalf("missing %q in ssh -G: %s", want, out)
			}
		}
		expected := "controlpath " + filepath.Join(base, name, "control.sock")
		if !strings.Contains(string(out), expected+"\n") {
			t.Fatalf("missing isolated path %q: %s", expected, out)
		}
		controls = append(controls, expected)
	}
	if controls[0] == controls[1] {
		t.Fatal("shared control path")
	}
	// A disabled interval is emitted explicitly, rather than inheriting user defaults.
	options := DefaultSSHConfigOptions()
	options.ServerAliveInterval = 0
	path, err := WriteConfigWithOptions(directTicket(t), filepath.Join(base, "disabled"), "/usr/bin/env", options)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(ssh, "-G", "-F", path, "shellpier").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "serveraliveinterval 0\n") {
		t.Fatalf("interval not disabled: %s", out)
	}
}
