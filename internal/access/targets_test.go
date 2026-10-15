package access

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/platform"
)

func targetFixture(t *testing.T) (string, string, Ticket) {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "private config")
	ticket, _, err := NewPair(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ticket.Addresses = []string{"127.0.0.1:2222"}
	file := filepath.Join(base, "中文 ticket file.json")
	writeTargetTicket(t, file, ticket)
	return dir, file, ticket
}
func writeTargetTicket(t *testing.T, file string, ticket Ticket) {
	t.Helper()
	data, err := json.Marshal(ticket)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = platform.SecureFile(file); err != nil {
		t.Fatal(err)
	}
}
func TestTargetsLifecycle(t *testing.T) {
	dir, file, ticket := targetFixture(t)
	t.Setenv("SHELLPIER_CONFIG_DIR", dir)
	info, err := AddTarget("", "dev-box", file)
	if err != nil || info.Status != "active" {
		t.Fatalf("add: %+v %v", info, err)
	}
	if _, err = AddTarget(dir, "dev-box", file); err == nil {
		t.Fatal("alias overwritten")
	}
	stored, err := os.ReadFile(filepath.Join(dir, "targets", "dev-box.json"))
	if err != nil || strings.Contains(string(stored), "private_key") || strings.Contains(string(stored), "sp1_") {
		t.Fatal("reference copied credentials")
	}
	resolved, err := Read("target:dev-box")
	if err != nil || resolved.ID != ticket.ID {
		t.Fatalf("resolve: %v", err)
	}
	list, err := ListTargets(dir)
	if err != nil || len(list) != 1 || list[0].Name != "dev-box" {
		t.Fatalf("list: %+v %v", list, err)
	}
	ticket.ExpiresAt = time.Now().Add(-time.Hour)
	writeTargetTicket(t, file, ticket)
	info, err = ShowTarget(dir, "dev-box")
	if err != nil || info.Status != "expired" {
		t.Fatalf("expired: %+v %v", info, err)
	}
	if _, err = Read("target:dev-box"); err == nil {
		t.Fatal("expired target accepted")
	}
	if err = os.WriteFile(file, []byte(`{"private_key":"SECRET"}`), 0600); err != nil {
		t.Fatal(err)
	}
	info, err = ShowTarget(dir, "dev-box")
	if err != nil || info.Status != "invalid" {
		t.Fatalf("invalid: %+v %v", info, err)
	}
	if err = os.Remove(file); err != nil {
		t.Fatal(err)
	}
	info, err = ShowTarget(dir, "dev-box")
	if err != nil || info.Status != "missing" {
		t.Fatalf("missing: %+v %v", info, err)
	}
	writeTargetTicket(t, file, ticket)
	if err = RemoveTarget(dir, "dev-box"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(file); err != nil {
		t.Fatal("remove deleted ticket")
	}
}
func TestTargetsRejectUnsafeReferences(t *testing.T) {
	dir, file, _ := targetFixture(t)
	for _, name := range []string{"", "../bad", "a/b", "a\\b", ".", "target:x", "中文", "CON", "COM1", strings.Repeat("x", 65)} {
		if _, err := AddTarget(dir, name, file); err == nil {
			t.Fatalf("accepted alias %q", name)
		}
	}
	if _, err := AddTarget(dir, "cycle", "target:cycle"); err == nil {
		t.Fatal("alias cycle accepted")
	}
	if _, err := AddTarget(dir, "first", file); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "targets", "first.json")
	data := []byte(`{"version":1,"ticket_file":"target:first"}`)
	if err := os.WriteFile(alias, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveTarget(dir, "first"); err == nil {
		t.Fatal("stored alias cycle accepted")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(file, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := AddTarget(dir, "public-ticket", file); err == nil {
			t.Fatal("public ticket accepted")
		}
		os.Chmod(file, 0600)
		link := filepath.Join(filepath.Dir(file), "ticket-link.json")
		if err := os.Symlink(file, link); err != nil {
			t.Fatal(err)
		}
		if _, err := AddTarget(dir, "link", link); err == nil {
			t.Fatal("symlink ticket accepted")
		}
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(alias, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := ResolveTarget(dir, "first"); err == nil {
			t.Fatal("public reference accepted")
		}
		if err := os.Chmod(alias, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if _, err := ListTargets(dir); err == nil {
			t.Fatal("public config accepted")
		}
		os.Chmod(dir, 0700)
	}
}
func TestTargetsDirectoryReplacement(t *testing.T) {
	dir, file, _ := targetFixture(t)
	if _, err := AddTarget(dir, "first", file); err != nil {
		t.Fatal(err)
	}
	root, verify, err := openTargets(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	folder := filepath.Join(dir, "targets")
	if err = os.Rename(folder, folder+"-old"); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	platform.SecureDir(folder)
	if err = verify(); err == nil {
		t.Fatal("replacement directory not detected")
	}
	if runtime.GOOS != "windows" {
		os.Remove(folder)
		if err = os.Symlink(folder+"-old", folder); err != nil {
			t.Fatal(err)
		}
		if _, err = ResolveTarget(dir, "first"); err == nil {
			t.Fatal("symlink config accepted")
		}
	}
}

func TestTargetsHostReference(t *testing.T) {
	dir, file, _ := targetFixture(t)
	_, host, err := NewPair(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	host.RelayURL = "https://relay.example.com"
	host.RelayToken = strings.Repeat("x", 64)
	writeTargetTicket(t, file, host)
	info, err := AddTarget(dir, "host", file)
	if err != nil || info.Kind != "host" {
		t.Fatalf("host alias: %+v %v", info, err)
	}
	t.Setenv("SHELLPIER_CONFIG_DIR", dir)
	got, err := Read("target:host")
	if err != nil {
		t.Fatal(err)
	}
	if got.Validate("access") == nil {
		t.Fatal("host alias crossed role boundary")
	}
}
