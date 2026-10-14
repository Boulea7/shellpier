package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestSchemaOfflineSelectionAndCapabilities(t *testing.T) {
	var out bytes.Buffer
	if err := schema([]string{"exec", "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var result struct {
		SchemaVersion int            `json:"schema_version"`
		Commands      []commandSpec  `json:"commands"`
		Capabilities  map[string]any `json:"capabilities"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != 1 || len(result.Commands) != 1 || result.Commands[0].Name != "exec" {
		t.Fatalf("bad schema %+v", result)
	}
	if result.Capabilities["os"] != runtime.GOOS || result.Capabilities["arch"] != runtime.GOARCH || result.Capabilities["scope"] != "local_controller" {
		t.Fatal("capabilities do not describe the actual controller")
	}
	if err := schema([]string{"unknown", "--json"}, io.Discard, io.Discard); err == nil {
		t.Fatal("accepted unknown command")
	}
	if err := schema([]string{"--help"}, io.Discard, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatal("help did not use flag help contract")
	}
	if err := schema([]string{"--json"}, completionFailWriter{io.ErrClosedPipe}, io.Discard); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal("writer error lost")
	}
}
func TestSchemaRegistryCoverage(t *testing.T) {
	seen := map[string]bool{}
	for _, spec := range commandSpecs {
		if seen[spec.Name] || spec.Name == "" || spec.Summary == "" || spec.Usage == "" || spec.StdioMode == "" || spec.Output == "" || spec.Flags == nil || spec.Effects == nil {
			t.Fatalf("incomplete or duplicate specification %q", spec.Name)
		}
		seen[spec.Name] = true
		names := map[string]bool{}
		for _, f := range spec.Flags {
			if names[f.Name] || f.Name == "" || f.Type == "" || f.Description == "" {
				t.Fatalf("invalid flag for %s", spec.Name)
			}
			names[f.Name] = true
		}
	}
	for _, name := range strings.Fields("share host invite connect proxy ssh-config doctor status stop revoke cleanup relay inspect check upload download keygen completion version help shell exec terminal files sessions schema capabilities") {
		if !seen[name] {
			t.Errorf("missing command %s", name)
		}
	}
}

// Help rendering is checked against discovery metadata; it is not its source.
func TestSchemaExistingHelpFlagsAgree(t *testing.T) {
	names := strings.Fields("share host invite connect proxy ssh-config doctor status stop revoke cleanup relay inspect check upload download keygen version exec shell capabilities sessions schema")
	expression := regexp.MustCompile(`(?m)^\s+-([a-z][a-z0-9-]*)\b`)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			if err := Run(context.Background(), []string{name, "--help"}, strings.NewReader(""), &out, &diagnostics, "test"); err != nil {
				t.Fatal(err)
			}
			actual := map[string]bool{}
			for _, m := range expression.FindAllStringSubmatch(diagnostics.String()+out.String(), -1) {
				actual[m[1]] = true
			}
			spec, _ := findCommandSpec(name)
			for _, f := range spec.Flags {
				if !actual[f.Name] {
					t.Errorf("schema flag --%s missing from actual help", f.Name)
				}
				delete(actual, f.Name)
			}
			if len(actual) > 0 {
				t.Errorf("actual flags omitted from schema: %v", actual)
			}
		})
	}
}
func TestSchemaHostDefaultsAgree(t *testing.T) {
	spec, _ := findCommandSpec("share")
	expected := map[string]string{"ttl": defaultSessionTTL.String(), "reconnect-min": defaultReconnectMin.String(), "reconnect-max": defaultReconnectMax.String()}
	for _, f := range spec.Flags {
		if value, ok := expected[f.Name]; ok && f.Default != value {
			t.Errorf("%s: schema %s runtime %s", f.Name, f.Default, value)
		}
	}
}

func TestSchemaNestedHelpFlagsAgree(t *testing.T) {
	expression := regexp.MustCompile(`(?m)^\s+-([a-z][a-z0-9-]*)\b`)
	for _, parent := range []string{"files", "terminal"} {
		spec, _ := findCommandSpec(parent)
		for _, sub := range spec.Subcommands {
			t.Run(parent+"_"+sub.Name, func(t *testing.T) {
				var out, diagnostics bytes.Buffer
				if err := Run(context.Background(), []string{parent, sub.Name, "--help"}, strings.NewReader(""), &out, &diagnostics, "test"); err != nil {
					t.Fatal(err)
				}
				actual := map[string]bool{}
				for _, m := range expression.FindAllStringSubmatch(diagnostics.String()+out.String(), -1) {
					actual[m[1]] = true
				}
				for _, flag := range sub.Flags {
					if !actual[flag.Name] {
						t.Errorf("schema flag --%s missing from help", flag.Name)
					}
					delete(actual, flag.Name)
				}
				if len(actual) > 0 {
					t.Errorf("undocumented flags: %v", actual)
				}
			})
		}
	}
}
