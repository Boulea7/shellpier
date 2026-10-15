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

func TestServiceRootPlanAndLifecycle(t *testing.T) {
	for _, osname := range []string{"linux", "darwin", "windows"} {
		for _, readonly := range []bool{false, true} {
			t.Run(osname+map[bool]string{false: "_write", true: "_read"}[readonly], func(t *testing.T) {
				env, dir, ticket, calls := serviceFixture(t, osname)
				root := filepath.Join(t.TempDir(), "共享 folder & 'quoted'")
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
				t.Chdir(filepath.Dir(root))
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatal(err)
				}
				relative, err := filepath.Rel(cwd, root)
				if err != nil {
					t.Fatal(err)
				}
				opts := []string{"--ticket", ticket, "--root", relative, "--allow-jobs", "--allow-forward", "localhost:12345"}
				mode := "sftp"
				if readonly {
					mode = "sftp-readonly"
					opts = append(opts, "--access-mode", mode)
				}
				var out bytes.Buffer
				run := func(action string, options ...string) error {
					out.Reset()
					args := append([]string{action, "demo", "--config-dir", dir, "--json"}, options...)
					return serviceWith(context.Background(), args, &out, io.Discard, env)
				}
				if err := run("plan", opts...); err != nil {
					t.Fatal(err)
				}
				var plan struct {
					Service    serviceManifest `json:"service"`
					Definition string          `json:"definition"`
				}
				if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
					t.Fatal(err)
				}
				if plan.Service.Version != 2 || plan.Service.FileRoot != root || plan.Service.AccessMode != mode || plan.Service.AllowJobs || len(plan.Service.AllowForward) != 0 || !strings.Contains(plan.Definition, "--root") || !strings.Contains(plan.Definition, "--allow-jobs=false") {
					t.Fatalf("root plan lost policy: %+v", plan)
				}
				if len(*calls) != 0 {
					t.Fatal("plan invoked manager")
				}
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatal("plan created config")
				}
				if err := run("install", opts...); err != nil {
					t.Fatal(err)
				}
				manifest, err := readServiceManifest(filepath.Join(dir, "services", "demo", "manifest.json"), env, "demo")
				if err != nil || manifest.FileRoot != root || manifest.AccessMode != mode || manifest.AllowJobs {
					t.Fatalf("installed root: %+v %v", manifest, err)
				}
				if err := run("start"); err != nil {
					t.Fatal(err)
				}
				if err := run("uninstall"); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(root); err != nil {
					t.Fatal("uninstall removed shared root")
				}
				if _, err := os.Stat(ticket); err != nil {
					t.Fatal("uninstall removed ticket")
				}
			})
		}
	}
}

func TestServiceRootInvalidBeforeOutput(t *testing.T) {
	env, dir, _, calls := serviceFixture(t, "linux")
	var out bytes.Buffer
	err := serviceWith(context.Background(), []string{"install", "demo", "--ticket", "missing-ticket", "--config-dir", dir, "--root", filepath.Join(t.TempDir(), "missing")}, &out, io.Discard, env)
	if err == nil || !strings.Contains(err.Error(), "shared directory") || out.Len() != 0 || len(*calls) != 0 {
		t.Fatalf("root validation ran late: %q %v %v", out.String(), err, *calls)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("invalid root created config")
	}
}

func TestServiceJobsExplicitFalseV2AndV1Rendering(t *testing.T) {
	env, dir, ticket, _ := serviceFixture(t, "linux")
	var out bytes.Buffer
	var defaults bytes.Buffer
	if err := serviceWith(context.Background(), []string{"plan", "demo", "--ticket", ticket, "--config-dir", dir, "--json"}, &defaults, io.Discard, env); err != nil {
		t.Fatal(err)
	}
	var defaultPlan struct {
		Service serviceManifest `json:"service"`
	}
	if err := json.Unmarshal(defaults.Bytes(), &defaultPlan); err != nil || !defaultPlan.Service.AllowJobs || defaultPlan.Service.AccessMode != "full" {
		t.Fatalf("default service jobs: %s %v", defaults.String(), err)
	}
	if err := serviceWith(context.Background(), []string{"plan", "demo", "--ticket", ticket, "--config-dir", dir, "--allow-jobs=false", "--json"}, &out, io.Discard, env); err != nil {
		t.Fatal(err)
	}
	var plan struct {
		Service    serviceManifest `json:"service"`
		Definition string          `json:"definition"`
	}
	if err := json.Unmarshal(out.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Service.Version != 2 || plan.Service.AllowJobs || !strings.Contains(plan.Definition, "--allow-jobs=false") {
		t.Fatal("v2 false was not explicit")
	}
	legacy := plan.Service
	legacy.Version = 1
	if strings.Contains(strings.Join(serviceArgs(legacy), " "), "--allow-jobs") {
		t.Fatal("v1 rendering changed")
	}
}
