package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Boulea7/shellpier/internal/platform"
	"golang.org/x/crypto/ssh"
)

func TestKeygenParseAndSign(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	var output bytes.Buffer
	if err := keygen([]string{"--out", dir, "--json"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	var result keygenResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	private, err := os.ReadFile(result.PrivateKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.ParsePrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	public, err := os.ReadFile(result.PublicKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	key, _, _, rest, err := ssh.ParseAuthorizedKey(public)
	if err != nil || len(rest) != 0 {
		t.Fatalf("public key parse: %v", err)
	}
	message := []byte("ShellPier key verification")
	signature, err := signer.Sign(nil, message)
	if err != nil {
		t.Fatal(err)
	}
	if err := key.Verify(message, signature); err != nil {
		t.Fatal(err)
	}
	if result.Fingerprint != ssh.FingerprintSHA256(key) {
		t.Fatal("incorrect fingerprint")
	}
	if strings.Contains(output.String(), "PRIVATE KEY") || bytes.Contains(output.Bytes(), private) {
		t.Fatal("private key printed")
	}

	if runtime.GOOS != "windows" {
		for path, want := range map[string]os.FileMode{dir: 0700, result.PrivateKeyPath: 0600, result.PublicKeyPath: 0600} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != want {
				t.Fatalf("%s mode %o want %o", path, info.Mode().Perm(), want)
			}
		}
	}
}
func TestKeygenNoOverwrite(t *testing.T) {
	parent := t.TempDir()
	for _, name := range []string{"existing-directory", "existing-file"} {
		path := filepath.Join(parent, name)
		if name == "existing-directory" {
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
		} else {
			if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := generateKeyPair(path, platform.SecureFile); err == nil {
			t.Fatal("existing output accepted")
		}
		if name == "existing-file" {
			data, _ := os.ReadFile(path)
			if string(data) != "preserve" {
				t.Fatal("file overwritten")
			}
		}
	}
}
func TestKeygenBadArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"--out"}, {"--unknown"}, {"--out", "somewhere", "extra"}} {
		var out bytes.Buffer
		if err := keygen(args, &out, &out); err == nil {
			t.Fatalf("invalid args accepted: %v", args)
		}
	}
	if _, err := generateKeyPair(filepath.Join(t.TempDir(), "missing", "keys"), platform.SecureFile); err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("missing parent error: %v", err)
	}
}
func TestKeygenRollback(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	failure := errors.New("injected ACL failure")
	calls := 0
	_, err := generateKeyPair(dir, func(path string) error {
		calls++
		if calls == 2 {
			return failure
		}
		return platform.SecureFile(path)
	})
	if !errors.Is(err, failure) {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatal("failed key pair directory survived")
	}
}
func TestKeygenRollbackPreservesUnknownAndReplacement(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	failure := errors.New("injected failure")
	_, err := generateKeyPair(dir, func(path string) error {
		if filepath.Base(path) == "identity.pub" {
			private := filepath.Join(dir, "identity")
			if err := os.Rename(private, filepath.Join(dir, "original")); err != nil {
				return err
			}
			if err := os.WriteFile(private, []byte("replacement"), 0600); err != nil {
				return err
			}
			return failure
		}
		return platform.SecureFile(path)
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "identity"))
	if err != nil || string(data) != "replacement" {
		t.Fatal("replacement file removed")
	}
	if _, err := os.Stat(filepath.Join(dir, "original")); err != nil {
		t.Fatal("unknown file removed")
	}
	if _, err := os.Lstat(filepath.Join(dir, "identity.pub")); !os.IsNotExist(err) {
		t.Fatal("owned partial file survived")
	}
}
