package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestMCPHostTerminalsDiscoveryAndArguments(t *testing.T) {
	tools := map[string]mcpTool{}
	for _, tool := range mcpTools() {
		tools[tool.Name] = tool
	}
	for _, action := range strings.Fields("open read write status list resize signal close operation") {
		t.Run(action, func(t *testing.T) {
			tool, ok := tools["shellpier_terminals_"+action]
			if !ok {
				t.Fatal("tool not discovered")
			}
			readOnly := action == "read" || action == "status" || action == "list" || action == "operation"
			if tool.Annotations["readOnlyHint"] != readOnly || tool.Annotations["destructiveHint"] != !readOnly {
				t.Fatalf("annotations: %v", tool.Annotations)
			}
			effects := tool.Meta["io.shellpier/effects"].([]string)
			if !containsTerminalEffect(effects, "network") || containsTerminalEffect(effects, "remote_write") == readOnly {
				t.Fatalf("effects: %v", effects)
			}
			input := map[string]any{"ticket": "@ticket"}
			if action != "open" && action != "list" {
				input["positionals"] = []string{"identity"}
			}
			mutation := action == "open" || action == "write"
			if mutation {
				raw, _ := json.Marshal(input)
				if _, err := tool.arguments(raw); err == nil {
					t.Fatal("missing operation ID accepted")
				}
				input["options"] = map[string]any{"operation-id": " "}
				raw, _ = json.Marshal(input)
				if _, err := tool.arguments(raw); err == nil {
					t.Fatal("blank operation ID accepted")
				}
				input["options"] = map[string]any{"operation-id": "stable"}
				properties := tool.InputSchema["properties"].(map[string]any)
				required := properties["options"].(map[string]any)["required"]
				if !reflect.DeepEqual(required, []string{"operation-id"}) {
					t.Fatalf("operation ID schema: %v", required)
				}
			}
			raw, _ := json.Marshal(input)
			args, err := tool.arguments(raw)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"terminals", action, "--json"}
			if mutation {
				want = append(want, "--operation-id=stable")
			}
			want = append(want, "@ticket")
			if action != "open" && action != "list" {
				want = append(want, "identity")
			}
			if !reflect.DeepEqual(args, want) {
				t.Fatalf("arguments %v, want %v", args, want)
			}
			if action != "open" && action != "list" {
				delete(input, "positionals")
				raw, _ = json.Marshal(input)
				if _, err := tool.arguments(raw); err == nil {
					t.Fatal("missing identity accepted")
				}
			}
			input["positionals"] = []string{"extra", "extra"}
			raw, _ = json.Marshal(input)
			if _, err := tool.arguments(raw); err == nil {
				t.Fatal("extra positional accepted")
			}
		})
	}
	copyTool := tools["shellpier_files_copy"]
	for count := 0; count <= 3; count++ {
		input := map[string]any{"ticket": "@ticket", "positionals": strings.Fields(strings.Repeat("path ", count))}
		raw, _ := json.Marshal(input)
		_, err := copyTool.arguments(raw)
		if (err == nil) != (count == 2) {
			t.Fatalf("copy count %d: %v", count, err)
		}
	}
}

func containsTerminalEffect(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func TestMCPHostTerminalsFileStdinRejectedWithoutConsumingProtocol(t *testing.T) {
	calls := 0
	runner := func(_ context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, _ string) error {
		calls++
		if args[0] != "version" {
			t.Errorf("rejected tool invoked runner: %v", args)
		}
		_, err := io.WriteString(stdout, `{"version":"test"}`)
		return err
	}
	client := startMCPTest(t, runner, mcpTestOptions())
	client.send(t, mcpCallRequest("write", "shellpier_terminals_write", map[string]any{"ticket": "@ticket", "positionals": []string{"terminal"}, "options": map[string]any{"operation-id": "stable", "file": "-"}}))
	rejected := client.read(t)
	if rejected["error"] == nil {
		result := mcpResultObject(t, rejected)
		if result["isError"] != true {
			t.Fatalf("stdin accepted: %v", rejected)
		}
	}
	client.send(t, mcpCallRequest("version", "shellpier_version", map[string]any{}))
	response := client.read(t)
	if response["id"] != "version" || mcpResultObject(t, response)["isError"] != false {
		t.Fatalf("protocol consumed: %v", response)
	}
	if calls != 1 {
		t.Fatalf("runner calls: %d", calls)
	}
}

func TestMCPHostTerminalsUnknownOutcomeSurvivesCancellation(t *testing.T) {
	for _, action := range []string{"open", "write"} {
		t.Run(action, func(t *testing.T) {
			cause := errors.New("private credential cause")
			unknown := &TerminalOperationUnknownError{Action: action, OperationID: "stable", TerminalID: "terminal", Err: cause}
			wrapped := fmt.Errorf("transport: %w", unknown)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			server := mcpServer{options: mcpTestOptions(), run: func(context.Context, []string, io.Reader, io.Writer, io.Writer, string) error {
				cancel()
				return wrapped
			}}
			result := server.execute(ctx, mcpTool{command: []string{"terminals", action}}, nil)
			if !result.IsError {
				t.Fatal("unknown result reported successful")
			}
			data := result.Structured
			if data["action"] != action || data["operation_id"] != "stable" || data["terminal_id"] != "terminal" || data["outcome"] != "unknown" || data["retry_safe"] != false {
				t.Fatalf("unknown identity lost: %v", data)
			}
			detail := classifyJSONError(wrapped)
			if detail.Code != "outcome_unknown" || detail.Action != action || detail.OperationID != "stable" || detail.TerminalID != "terminal" || detail.Outcome != "unknown" {
				t.Fatalf("JSON detail: %+v", detail)
			}
			if !reflect.DeepEqual(data["error"], detail) {
				t.Fatalf("MCP detail differs: %v", data["error"])
			}
			var output bytes.Buffer
			reported := WriteJSONError(&output, "terminals", wrapped)
			if !errors.Is(reported, cause) {
				t.Fatal("diagnostic lost underlying cause")
			}
			var payload struct {
				Error jsonErrorDetail `json:"error"`
			}
			if err := json.Unmarshal(output.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(payload.Error, detail) {
				t.Fatalf("emitted detail differs: %+v", payload.Error)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), cause.Error()) || strings.Contains(output.String(), cause.Error()) {
				t.Fatal("private cause leaked")
			}
		})
	}
}

func TestHostTerminalsCapabilityValidation(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		protocol   int
		pty, want  bool
	}{
		{"legacy omitted mode", "", 0, false, true}, {"legacy full", "full", 0, true, true}, {"current full", "full", 1, true, true}, {"current no PTY", "full", 1, false, false}, {"restricted", "sftp", 1, false, false}, {"restricted PTY", "sftp", 1, true, false}, {"future protocol", "full", 2, true, false}, {"negative protocol", "full", -1, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := remoteCapabilities{Protocol: 1, OS: "linux", Arch: "amd64", Shell: "sh", SFTP: true, AccessMode: tc.mode, HostTerminalsProtocol: tc.protocol, PTY: tc.pty}
			err := validateRemoteCapabilities(&c)
			if (err == nil) != tc.want {
				t.Fatalf("validation: %v", err)
			}
			if tc.want && c.AccessMode != "full" {
				t.Fatalf("legacy mode not normalized: %s", c.AccessMode)
			}
		})
	}
}
