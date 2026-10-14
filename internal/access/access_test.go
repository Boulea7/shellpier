package access

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Boulea7/shellpier/internal/platform"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func directTicket(t *testing.T) Ticket {
	t.Helper()
	a, h, e := NewPair(time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	if a.PrivateKey == h.HostPrivateKey || a.ClientKey != "" || h.HostKey != "" {
		t.Fatal("role separation")
	}
	a.Addresses = []string{"127.0.0.1:2222"}
	return a
}
func TestTicketRoundTrip(t *testing.T) {
	a := directTicket(t)
	token, e := a.Encode()
	if e != nil {
		t.Fatal(e)
	}
	b, e := Parse(token)
	if e != nil || b.PrivateKey != a.PrivateKey || b.HostKey != a.HostKey {
		t.Fatal("roundtrip failed", e)
	}
	if b.Validate("host") == nil {
		t.Fatal("role accepted")
	}
	if _, e := Read(token); e != nil {
		t.Fatal(e)
	}
	a.ExpiresAt = time.Now().Add(-time.Second)
	if _, e := a.Encode(); e == nil {
		t.Fatal("expired accepted")
	}
}
func TestPersistentAndHost(t *testing.T) {
	a, h, e := NewPair(0)
	if e != nil || !a.ExpiresAt.IsZero() || !h.ExpiresAt.IsZero() {
		t.Fatal("persistent failed", e)
	}
	if _, e := a.Encode(); e == nil {
		t.Fatal("unconfigured accepted")
	}
	h.RelayURL = "https://example.com"
	h.RelayToken = h.ID
	if _, e := h.Encode(); e != nil {
		t.Fatal(e)
	}
	h.PrivateKey = a.PrivateKey
	if h.Validate("host") == nil {
		t.Fatal("mixed role accepted")
	}
}
func TestInvalidTickets(t *testing.T) {
	a := directTicket(t)
	for _, edit := range []func(*Ticket){func(t *Ticket) { t.Version = 2 }, func(t *Ticket) { t.ID = "../../etc" }, func(t *Ticket) { t.HostPrivateKey = t.PrivateKey }, func(t *Ticket) { t.HostKey = "ssh-ed25519 BAD" }, func(t *Ticket) { t.PrivateKey = "bad" }, func(t *Ticket) { t.Addresses = []string{"localhost:0"} }, func(t *Ticket) { t.Addresses = []string{"a;echo:22"} }, func(t *Ticket) { t.RelayURL = "http://example.com"; t.RelayToken = t.ID }, func(t *Ticket) { t.RelayURL = "https://user:pass@example.com"; t.RelayToken = t.ID }} {
		b := a
		edit(&b)
		if b.Validate("") == nil {
			t.Fatal("invalid ticket accepted")
		}
	}
	for _, s := range []string{"", "sp1_bad=", strings.Repeat("x", maxTicket+1)} {
		if _, e := Parse(s); e == nil {
			t.Fatal("invalid token accepted")
		}
	}
}
func TestReadRegularAndBounded(t *testing.T) {
	a := directTicket(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "ticket")
	b, _ := json.Marshal(a)
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Read("@" + path); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(dir, "link")
	if e := os.Symlink(path, link); e == nil {
		if _, e := Read(link); e == nil {
			t.Fatal("symlink accepted")
		}
	}
	if _, e := Read("/dev/zero"); e == nil {
		t.Fatal("device accepted")
	}
	os.WriteFile(path, make([]byte, maxTicket+1), 0600)
	if _, e := Read(path); e == nil {
		t.Fatal("oversize accepted")
	}
}
func TestWriteConfig(t *testing.T) {
	a := directTicket(t)
	dir := filepath.Join(t.TempDir(), "配置 space ' quote % \" double")
	if runtime.GOOS == "windows" {
		dir = filepath.Join(t.TempDir(), "配置 space")
	}
	config, e := WriteConfig(a, dir, "/usr/bin/env")
	if e != nil {
		t.Fatal(e)
	}
	if _, e := WriteConfig(a, dir, "/usr/bin/env"); e == nil {
		t.Fatal("overwrote directory")
	}
	for _, file := range append(configFiles, "shellpier-manifest.json") {
		if _, e := os.Stat(filepath.Join(dir, file)); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := Read(filepath.Join(dir, "access.json")); e != nil {
		t.Fatal(e)
	}
	if ssh, err := exec.LookPath("ssh"); err == nil {
		cmd := exec.CommandContext(context.Background(), ssh, "-G", "-F", config, "shellpier")
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("SSH config parse: %v %s", e, out)
		}
		if !strings.Contains(string(out), "hostkeyalias shellpier-"+strings.ToLower(a.ID)) {
			t.Fatalf("pin alias missing: %s", out)
		}
	}
}
func TestWindowsPathSafety(t *testing.T) {
	for _, path := range []string{`C:\Program Files\中文\shellpier.exe`} {
		if _, e := proxyCommand(path, `C:\用户\ticket.json`, true); e != nil {
			t.Fatal(e)
		}
	}
	for _, bad := range []string{`a" & echo pwn`, "%PATH%", "foo!", "foo\nbar", "a(b)", "a|b"} {
		if _, e := proxyCommand(bad, "safe", true); e == nil {
			t.Fatal("unsafe Windows path accepted")
		}
	}
	if _, e := configQuote("a\nb"); e == nil {
		t.Fatal("newline accepted")
	}
}

func TestProxyCommandPreservesPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell test")
	}
	sshPath, e := exec.LookPath("ssh")
	if e != nil {
		t.Skip("OpenSSH unavailable")
	}
	a := directTicket(t)
	root := t.TempDir()
	dir := filepath.Join(root, "资料 ' quote \" double % $ ; &(test)")
	binary := filepath.Join(root, "fake ' executable \" % $; .sh")
	script := "#!/bin/sh\nprintf '%s' \"$2\" > \"$SHELLPIER_TEST_MARKER\"\nexit 1\n"
	if e = os.WriteFile(binary, []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	config, e := WriteConfig(a, dir, binary)
	if e != nil {
		t.Fatal(e)
	}
	marker := filepath.Join(root, "marker")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, sshPath, "-F", config, "shellpier", "true")
	cmd.Env = append(os.Environ(), "SHELLPIER_TEST_MARKER="+marker)
	_ = cmd.Run()
	data, e := os.ReadFile(marker)
	if e != nil {
		t.Fatal("proxy executable did not execute", e)
	}
	if string(data) != "@"+filepath.Join(dir, "access.json") {
		t.Fatalf("proxy changed path: %q", data)
	}
}

func TestConfigRollbackPreservesUnknownAndConflict(t *testing.T) {
	a := directTicket(t)
	for _, conflict := range []bool{false, true} {
		dir := filepath.Join(t.TempDir(), "output")
		var manifestEmpty bool
		secure := func(path string) error {
			if filepath.Base(path) == "ssh_config" {
				if e := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("keep"), 0600); e != nil {
					return e
				}
				if conflict {
					return os.WriteFile(filepath.Join(dir, "shellpier-manifest.json"), []byte("preexisting"), 0600)
				}
			}
			if filepath.Base(path) == "shellpier-manifest.json" {
				b, e := os.ReadFile(path)
				manifestEmpty = e == nil && len(b) == 0
				return errors.New("injected ACL failure")
			}
			return platform.SecureFile(path)
		}
		if _, e := writeConfig(a, dir, "/bin/true", secure); e == nil {
			t.Fatal("injected error not returned")
		}
		for _, name := range configFiles {
			if _, e := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(e) {
				t.Fatal("owned file left", name, e)
			}
		}
		if b, e := os.ReadFile(filepath.Join(dir, "unrelated.txt")); e != nil || string(b) != "keep" {
			t.Fatal("unrelated file affected", e)
		}
		b, e := os.ReadFile(filepath.Join(dir, "shellpier-manifest.json"))
		if conflict {
			if e != nil || string(b) != "preexisting" {
				t.Fatal("conflicting file removed", e)
			}
		} else {
			if !os.IsNotExist(e) || !manifestEmpty {
				t.Fatal("failed-created manifest not removed or secret write preceded ACL", e)
			}
		}
	}
}
func TestConfigRollbackRemovesEmptyDirectory(t *testing.T) {
	a := directTicket(t)
	dir := filepath.Join(t.TempDir(), "output")
	secure := func(path string) error {
		b, e := os.ReadFile(path)
		if e != nil || len(b) != 0 {
			t.Fatal("secret bytes written before permission enforcement")
		}
		return errors.New("injected failure")
	}
	if _, e := writeConfig(a, dir, "/bin/true", secure); e == nil {
		t.Fatal("injected error not returned")
	}
	if _, e := os.Lstat(dir); !os.IsNotExist(e) {
		t.Fatal("empty owned directory remained", e)
	}
}
func TestBoundedReadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input")
	os.WriteFile(path, []byte("abc"), 0600)
	if b, e := ReadFile(path, 3); e != nil || string(b) != "abc" {
		t.Fatal("read failed", e)
	}
	if _, e := ReadFile(path, 2); e == nil {
		t.Fatal("oversize accepted")
	}
	if _, e := ReadFile(path, -1); e == nil {
		t.Fatal("negative limit accepted")
	}
}
