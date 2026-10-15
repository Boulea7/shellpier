package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/platform"
)

func serviceFixture(t *testing.T, osname string) (serviceEnvironment, string, string, *[]string) {
	t.Helper()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	_, host, err := access.NewPair(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	host.RelayURL = "https://relay.example.com"
	host.RelayToken = strings.Repeat("x", 64)
	ticket := filepath.Join(base, "host 中文 ticket.json")
	data, _ := json.Marshal(host)
	if err = os.WriteFile(ticket, data, 0600); err != nil {
		t.Fatal(err)
	}
	platform.SecureFile(ticket)
	calls := []string{}
	resources := map[string]string{}
	loaded := false
	env := serviceEnvironment{os: osname, home: home, exe: filepath.Join(base, "program with space"), uid: "1000"}
	env.run = func(ctx context.Context, exe string, args ...string) ([]byte, error) {
		calls = append(calls, exe+" "+strings.Join(args, " "))
		joined := strings.Join(args, " ")
		if exe == "systemctl" && strings.Contains(joined, "FragmentPath") {
			file := filepath.Join(home, ".config", "systemd", "user", "shellpier-host-demo.service")
			if _, err := os.Stat(file); err == nil {
				return []byte(file + "\n"), nil
			}
			return nil, nil
		}
		if exe == "systemctl" && strings.Contains(joined, "LoadState") {
			return []byte("not-found"), nil
		}
		if exe == "powershell.exe" && strings.Contains(joined, "SHELLPIER_NOT_REGISTERED") {
			if _, ok := resources["xml"]; !ok {
				return []byte("SHELLPIER_NOT_REGISTERED"), nil
			}
			return []byte("SHELLPIER_REGISTERED"), nil
		}
		if exe == "launchctl" && len(args) > 1 && args[0] == "print" && strings.Contains(args[1], "com.shellpier") {
			if !loaded {
				return []byte(`Could not find service "com.shellpier.host.demo" in domain`), errors.New("not loaded")
			}
			return []byte("path = " + resources["path"] + "\n"), nil
		}
		if exe == "launchctl" && args[0] == "bootstrap" {
			loaded = true
			resources["path"] = args[2]
		}
		if exe == "launchctl" && args[0] == "bootout" {
			loaded = false
		}
		if exe == "schtasks.exe" && len(args) > 1 && args[0] == "/Query" {
			data, ok := resources["xml"]
			if !ok {
				return nil, errors.New("not registered")
			}
			return []byte(data), nil
		}
		if exe == "schtasks.exe" && args[0] == "/Create" {
			data, err := os.ReadFile(args[len(args)-1])
			if err != nil {
				return nil, err
			}
			resources["xml"] = string(data)
		}
		return []byte("ready"), nil
	}
	return env, filepath.Join(base, "config"), ticket, &calls
}
func TestServicePlanAndInstallLifecycle(t *testing.T) {
	for _, osname := range []string{"linux", "darwin", "windows"} {
		t.Run(osname, func(t *testing.T) {
			env, dir, ticket, calls := serviceFixture(t, osname)
			var out bytes.Buffer
			run := func(args ...string) error {
				out.Reset()
				return serviceWith(context.Background(), append(args, "--config-dir", dir, "--json"), &out, io.Discard, env)
			}
			if err := run("plan", "demo", "--ticket", ticket, "--autostart", "--work-dir", "/a directory"); err != nil {
				t.Fatal(err)
			}
			if len(*calls) != 0 {
				t.Fatal("plan called manager")
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatal("plan wrote config")
			}
			if strings.Contains(out.String(), "host_private_key") || strings.Contains(out.String(), "relay_token") {
				t.Fatal("plan leaked ticket")
			}
			if err := run("install", "demo", "--ticket", ticket, "--autostart"); err != nil {
				t.Fatal(err)
			}
			for _, call := range *calls {
				if strings.Contains(call, "bootstrap") || strings.Contains(call, "/Run") || strings.Contains(call, "--user start") {
					t.Fatalf("install started service: %s", call)
				}
			}
			if err := run("install", "demo", "--ticket", ticket); err == nil {
				t.Fatal("install overwrote resource")
			}
			before := len(*calls)
			if err := run("start", "demo", "--dry-run"); err != nil {
				t.Fatal(err)
			}
			if len(*calls) != before {
				t.Fatal("dry-run called manager")
			}
			if err := run("start", "demo"); err != nil {
				t.Fatal(err)
			}
			if err := run("status", "demo"); err != nil {
				t.Fatal(err)
			}
			if err := run("stop", "demo"); err != nil {
				t.Fatal(err)
			}
			if err := run("uninstall", "demo"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(ticket); err != nil {
				t.Fatal("uninstall removed ticket")
			}
		})
	}
}
func TestServiceExpiryAndOwnership(t *testing.T) {
	env, dir, ticket, calls := serviceFixture(t, "linux")
	run := func(args ...string) error {
		return serviceWith(context.Background(), append(args, "--config-dir", dir), io.Discard, io.Discard, env)
	}
	if err := run("install", "demo", "--ticket", ticket); err != nil {
		t.Fatal(err)
	}
	manifest, err := readServiceManifest(filepath.Join(dir, "services", "demo", "manifest.json"), env, "demo")
	if err != nil {
		t.Fatal(err)
	}
	host, err := access.Read("@" + ticket)
	if err != nil {
		t.Fatal(err)
	}
	host.ExpiresAt = time.Now().Add(-time.Minute)
	data, _ := json.Marshal(host)
	os.WriteFile(ticket, data, 0600)
	before := len(*calls)
	if err = run("start", "demo"); err == nil {
		t.Fatal("started expired host")
	}
	if len(*calls) != before {
		t.Fatal("expired start reached manager")
	}
	if err = os.WriteFile(manifest.Resource, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = run("uninstall", "demo"); err == nil {
		t.Fatal("removed changed resource")
	}
	if _, err = os.Stat(manifest.Resource); err != nil {
		t.Fatal("changed file deleted")
	}
}
func TestServiceRenderingEscapes(t *testing.T) {
	m := serviceManifest{Name: "safe", Program: `/a space/quote"%$\\program`, TicketFile: `/a space/ticket"%$\\file`, Shell: "sh", Autostart: true, Marker: strings.Repeat("a", 32)}
	for _, osname := range []string{"darwin", "windows"} {
		text := renderService(m, serviceEnvironment{os: osname, uid: "user & identity"}, false)
		decoder := xml.NewDecoder(strings.NewReader(text))
		for {
			_, err := decoder.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("invalid %s XML: %v", osname, err)
			}
		}
		if strings.Contains(text, "<Password>") || strings.Contains(text, "<BootTrigger>") || strings.Contains(text, "RestartOnFailure") {
			t.Fatal("unsafe task settings")
		}
	}
	text := renderService(m, serviceEnvironment{os: "linux"}, false)
	if !strings.Contains(text, "%%$$") || !strings.Contains(text, "Restart=no") || strings.Contains(text, "Restart=always") {
		t.Fatal("systemd escaping or restart policy")
	}
}
func TestServiceHelpNoEffects(t *testing.T) {
	for _, action := range []string{"plan", "install", "start", "status", "stop", "uninstall"} {
		err := service(context.Background(), []string{action, "--help"}, nil, io.Discard, io.Discard)
		if !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("help: %v", err)
		}
	}
}

func TestServiceRefusesReplacedManagerResource(t *testing.T) {
	for _, osname := range []string{"linux", "darwin", "windows"} {
		t.Run(osname, func(t *testing.T) {
			env, dir, ticket, _ := serviceFixture(t, osname)
			args := []string{"install", "demo", "--ticket", ticket, "--config-dir", dir}
			if err := serviceWith(context.Background(), args, io.Discard, io.Discard, env); err != nil {
				t.Fatal(err)
			}
			original := env.run
			env.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				if osname == "linux" && strings.Contains(joined, "FragmentPath") {
					return []byte("/foreign/unit.service"), nil
				}
				if osname == "darwin" && args[0] == "print" && strings.Contains(joined, "com.shellpier") {
					return []byte("path = /foreign/agent.plist\n"), nil
				}
				if osname == "windows" && strings.Contains(joined, "/TN") && args[0] == "/Query" {
					return []byte(`<Task><RegistrationInfo><Description>foreign</Description></RegistrationInfo></Task>`), nil
				}
				return original(ctx, name, args...)
			}
			if err := serviceWith(context.Background(), []string{"uninstall", "demo", "--config-dir", dir}, io.Discard, io.Discard, env); err == nil {
				t.Fatal("controlled replaced service")
			}
			if _, err := os.Stat(filepath.Join(dir, "services", "demo", "manifest.json")); err != nil {
				t.Fatal("removed ownership manifest")
			}
		})
	}
}
func TestServiceHostAliasAndDryRun(t *testing.T) {
	env, dir, ticket, calls := serviceFixture(t, "linux")
	if _, err := access.AddTarget(dir, "host", ticket); err != nil {
		t.Fatal(err)
	}
	if err := serviceWith(context.Background(), []string{"install", "demo", "--ticket", "target:host", "--config-dir", dir, "--dry-run"}, io.Discard, io.Discard, env); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Fatal("dry-run invoked service manager")
	}
	if _, err := os.Stat(filepath.Join(dir, "services")); !os.IsNotExist(err) {
		t.Fatal("dry-run wrote services")
	}
}

func TestServiceStartRejectsChangedTicketIdentity(t *testing.T) {
	env, dir, ticket, calls := serviceFixture(t, "linux")
	if err := serviceWith(context.Background(), []string{"install", "demo", "--ticket", ticket, "--config-dir", dir}, io.Discard, io.Discard, env); err != nil {
		t.Fatal(err)
	}
	_, replacement, err := access.NewPair(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	replacement.RelayURL = "https://relay.example.com"
	replacement.RelayToken = strings.Repeat("r", 64)
	data, _ := json.Marshal(replacement)
	if err = os.WriteFile(ticket, data, 0600); err != nil {
		t.Fatal(err)
	}
	before := len(*calls)
	err = serviceWith(context.Background(), []string{"start", "demo", "--config-dir", dir}, io.Discard, io.Discard, env)
	if err == nil || !strings.Contains(err.Error(), "identity changed") || len(*calls) != before {
		t.Fatalf("changed ticket started or error unclear: %v", err)
	}
}
func TestServiceCleanupRequiresConfirmedAbsence(t *testing.T) {
	for _, osname := range []string{"linux", "darwin", "windows"} {
		for _, permissionDenied := range []bool{false, true} {
			t.Run(osname+fmt.Sprint(permissionDenied), func(t *testing.T) {
				env, dir, ticket, _ := serviceFixture(t, osname)
				original := env.run
				env.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
					if osname == "linux" && args[len(args)-1] == "daemon-reload" || osname == "windows" && args[0] == "/Create" {
						return nil, errors.New("registration failed")
					}
					return original(ctx, name, args...)
				}
				err := serviceWith(context.Background(), []string{"install", "demo", "--ticket", ticket, "--config-dir", dir}, io.Discard, io.Discard, env)
				if osname != "darwin" && err == nil {
					t.Fatal("expected registration failure")
				}
				if osname == "darwin" && err != nil {
					t.Fatal(err)
				}
				manifestPath := filepath.Join(dir, "services", "demo", "manifest.json")
				if _, err = os.Stat(manifestPath); err != nil {
					t.Fatal("registration failure did not retain manifest")
				}
				env.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
					joined := strings.Join(args, " ")
					if name == "systemctl" && strings.Contains(joined, "FragmentPath") {
						if permissionDenied {
							return nil, errors.New("permission denied")
						}
						return nil, nil
					}
					if name == "systemctl" && strings.Contains(joined, "LoadState") {
						return []byte("not-found"), nil
					}
					if name == "launchctl" && args[0] == "print" && strings.Contains(joined, "com.shellpier") {
						if permissionDenied {
							return []byte("permission denied"), errors.New("permission denied")
						}
						return []byte(`Could not find service "com.shellpier.host.demo" in domain`), errors.New("not found")
					}
					if name == "schtasks.exe" && args[0] == "/Query" && strings.Contains(joined, "/TN") {
						return nil, errors.New("query failed")
					}
					if name == "powershell.exe" {
						if permissionDenied {
							return nil, errors.New("permission denied")
						}
						return []byte("SHELLPIER_NOT_REGISTERED"), nil
					}
					if strings.Contains(joined, "--user stop") || strings.Contains(joined, "/Delete") || strings.Contains(joined, "/End") {
						t.Fatal("manager mutation for absent service")
					}
					return original(ctx, name, args...)
				}
				err = serviceWith(context.Background(), []string{"uninstall", "demo", "--config-dir", dir}, io.Discard, io.Discard, env)
				if permissionDenied {
					if err == nil {
						t.Fatal("permission failure treated as absence")
					}
					if _, e := os.Stat(manifestPath); e != nil {
						t.Fatal("permission error removed files")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if _, e := os.Stat(manifestPath); !os.IsNotExist(e) {
						t.Fatal("confirmed absence did not clean files")
					}
				}
				if _, e := os.Stat(ticket); e != nil {
					t.Fatal("cleanup removed ticket")
				}
			})
		}
	}
}

func TestServiceLaunchctlCanonicalParentAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix parent symlink fixture")
	}
	env, dir, ticket, _ := serviceFixture(t, "darwin")
	parent := filepath.Dir(dir)
	realParent := filepath.Join(parent, "real config parent")
	if err := os.Mkdir(realParent, 0700); err != nil {
		t.Fatal(err)
	}
	aliasParent := filepath.Join(parent, "alias config parent")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Fatal(err)
	}
	dir = filepath.Join(aliasParent, "config")
	original := env.run
	env.run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		output, err := original(ctx, name, args...)
		if name == "launchctl" && args[0] == "print" && err == nil && strings.Contains(string(output), "path = ") {
			registered := strings.TrimSpace(strings.TrimPrefix(string(output), "path = "))
			canonical, e := filepath.EvalSymlinks(registered)
			if e != nil {
				return nil, e
			}
			return []byte("job = {\n\tpath = " + canonical + "\n}\n"), nil
		}
		return output, err
	}
	for _, action := range []string{"install", "start", "status", "start", "stop", "uninstall"} {
		args := []string{action, "demo", "--config-dir", dir}
		if action == "install" {
			args = append(args, "--ticket", ticket)
		}
		if err := serviceWith(context.Background(), args, io.Discard, io.Discard, env); err != nil {
			t.Fatalf("%s with canonical manager path: %v", action, err)
		}
	}
}
func TestServiceLaunchctlPathRejectsSubstringAndDuplicate(t *testing.T) {
	file := filepath.Join(t.TempDir(), "owned agent.plist")
	if err := writePrivate(file, []byte("owned")); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(filepath.Dir(file), "foreign agent.plist")
	if err := writePrivate(foreign, []byte("foreign")); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{
		"program = " + file + "\npath = " + foreign + "\n",
		"path = " + file + ".unowned\n",
		"path = " + file + "\npath = " + file + "\n",
		"program = " + file + "\n",
	} {
		if serviceLaunchctlPath([]byte(output), file) == nil {
			t.Fatalf("accepted non-owned path: %q", output)
		}
	}
	canonical, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	if err = serviceLaunchctlPath([]byte("\tpath = "+canonical+"\n"), file); err != nil {
		t.Fatal(err)
	}
}
