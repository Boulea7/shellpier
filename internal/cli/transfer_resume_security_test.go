package cli

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/Boulea7/shellpier/internal/platform"
)

func TestResumeCacheSecuresOnlyNewDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "resume-cache")
	secured := 0
	secure := func(path string) error {
		secured++
		return platform.SecureDir(path)
	}
	if err := prepareResumeCacheDir(dir, secure); err != nil {
		t.Fatal(err)
	}
	if secured != 1 {
		t.Fatal("new directory was not secured exactly once")
	}
	if err := checkPrivateSessionDir(dir); err != nil {
		t.Fatalf("new cache lacks private owner permissions: %v", err)
	}
	if err := prepareResumeCacheDir(dir, func(string) error {
		t.Fatal("existing directory permissions must not be rewritten")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestResumeCacheSecurityFailurePropagates(t *testing.T) {
	want := errors.New("secure directory failed")
	got := prepareResumeCacheDir(filepath.Join(t.TempDir(), "cache"), func(string) error { return want })
	if !errors.Is(got, want) {
		t.Fatalf("security failure lost: %v", got)
	}
}
