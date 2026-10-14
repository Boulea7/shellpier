package access

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestReadFileSharesDeleteAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.DELETE, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	data, err := ReadFile(path, 100)
	if err != nil || string(data) != "old" {
		t.Fatalf("bounded reader refused delete-access handle: %q %v", data, err)
	}
}

func TestTicketReaderAllowsAtomicReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, err := openTicket(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	replacement := filepath.Join(dir, "new.json")
	if err := os.WriteFile(replacement, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	old, err := io.ReadAll(reader)
	if err != nil || string(old) != "old" {
		t.Fatalf("open handle lost original content: %q %v", old, err)
	}
	current, err := ReadFile(path, 100)
	if err != nil || string(current) != "new" {
		t.Fatalf("replacement unreadable: %q %v", current, err)
	}
}

func TestTicketMetadataKeepsOriginalIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	original, err := statTicket(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "new.json")
	if err := os.WriteFile(replacement, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	current, err := statTicket(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(original, current) {
		t.Fatal("metadata followed replacement pathname instead of original identity")
	}
}
