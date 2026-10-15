package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceRestrictedAccessPolicy(t *testing.T) {
	for _, osname := range []string{"linux", "darwin", "windows"} {
		t.Run(osname, func(t *testing.T) {
			env, dir, ticket, calls := serviceFixture(t, osname)
			var out bytes.Buffer
			run := func(args ...string) error {
				out.Reset()
				return serviceWith(context.Background(), append(args, "--config-dir", dir, "--json"), &out, io.Discard, env)
			}
			if err := run("plan", "demo", "--ticket", ticket, "--access-mode", "sftp-readonly"); err != nil {
				t.Fatal(err)
			}
			var plan struct {
				Service    serviceManifest `json:"service"`
				Definition string          `json:"definition"`
			}
			if err := json.Unmarshal(out.Bytes(), &plan); err != nil || plan.Service.AccessMode != "sftp-readonly" || !strings.Contains(plan.Definition, "--access-mode") || !strings.Contains(plan.Definition, "sftp-readonly") {
				t.Fatalf("plan lost restricted policy: %s", out.Bytes())
			}
			if len(*calls) != 0 {
				t.Fatal("plan reached service manager")
			}
			if err := run("install", "demo", "--ticket", ticket, "--access-mode", "sftp-readonly"); err != nil {
				t.Fatal(err)
			}
			manifestPath := filepath.Join(dir, "services", "demo", "manifest.json")
			m, err := readServiceManifest(manifestPath, env, "demo")
			if err != nil || m.AccessMode != "sftp-readonly" {
				t.Fatalf("installed policy: %+v %v", m, err)
			}
			if err = run("start", "demo"); err != nil {
				t.Fatal(err)
			}
			if err = run("uninstall", "demo"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestServiceAccessPolicyFailsBeforeTicketOrManager(t *testing.T) {
	cases := [][]string{
		{"--access-mode", "other"},
	}
	for _, opts := range cases {
		t.Run(strings.Join(opts, "_"), func(t *testing.T) {
			env, dir, _, calls := serviceFixture(t, "linux")
			args := append([]string{"install", "demo", "--ticket", filepath.Join(dir, "missing"), "--config-dir", dir}, opts...)
			err := serviceWith(context.Background(), args, io.Discard, io.Discard, env)
			if err == nil || (!strings.Contains(err.Error(), "access mode") && !strings.Contains(err.Error(), "access-mode")) {
				t.Fatalf("policy was not checked first: %v", err)
			}
			if len(*calls) != 0 {
				t.Fatal("invalid policy reached service manager")
			}
			if _, err = os.Lstat(dir); !os.IsNotExist(err) {
				t.Fatal("invalid policy created config")
			}
		})
	}
}

func TestServiceManifestAccessPolicyCompatibility(t *testing.T) {
	env, dir, ticket, calls := serviceFixture(t, "linux")
	run := func(action string) error {
		return serviceWith(context.Background(), []string{action, "demo", "--config-dir", dir}, io.Discard, io.Discard, env)
	}
	if err := serviceWith(context.Background(), []string{"install", "demo", "--ticket", ticket, "--config-dir", dir}, io.Discard, io.Discard, env); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "services", "demo", "manifest.json")
	m, err := readServiceManifest(path, env, "demo")
	if err != nil {
		t.Fatal(err)
	}
	// Old version-1 manifests omit access_mode and render the same full-access arguments.
	m.Version, m.AccessMode, m.AllowJobs = 1, "", false
	if err = os.WriteFile(path, mustServiceJSON(m), 0600); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{m.Resource: renderService(m, env, false), m.Registration: renderService(m, env, true)} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(strings.Join(serviceArgs(m), " "), "--allow-jobs") {
		t.Fatal("v1 rendering changed")
	}
	if err = run("start"); err != nil {
		t.Fatalf("legacy service changed semantics: %v", err)
	}
	before := len(*calls)
	m.AccessMode = "future-mode"
	if err = os.WriteFile(path, mustServiceJSON(m), 0600); err != nil {
		t.Fatal(err)
	}
	if err = run("start"); err == nil || len(*calls) != before {
		t.Fatal("unknown manifest policy reached manager")
	}
	m.AccessMode, m.AllowJobs = "sftp-readonly", false
	if err = os.WriteFile(path, mustServiceJSON(m), 0600); err != nil {
		t.Fatal(err)
	}
	if read, err := readServiceManifest(path, env, "demo"); err != nil || read.AccessMode != "sftp-readonly" {
		t.Fatalf("restricted legacy read failed: %+v %v", read, err)
	}
}
