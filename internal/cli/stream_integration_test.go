package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestStreamCLIRoundtripAndProgress(t *testing.T) {
	dir, ticket := slowTransferEndpoint(t)
	data := bytes.Repeat([]byte{0, 1, 255, '\n', '\r', 127}, 512)
	remote := transferPath(filepath.Join(dir, "cli-stream"))
	var out, diagnostic bytes.Buffer
	args := []string{"--json", "upload", "@" + ticket, "-", remote, "--progress=jsonl"}
	if err := Run(context.Background(), args, bytes.NewReader(data), &out, &diagnostic, "test"); err != nil {
		t.Fatal(err)
	}
	var result transferSummary
	if err := json.Unmarshal(out.Bytes(), &result); err != nil || result.Bytes != int64(len(data)) || result.Files != 1 {
		t.Fatalf("upload summary %s: %v", out.Bytes(), err)
	}
	assertStreamProgress(t, diagnostic.Bytes())
	out.Reset()
	diagnostic.Reset()
	args = []string{"download", "@" + ticket, remote, "-", "--progress=jsonl"}
	if err := Run(context.Background(), args, nil, &out, &diagnostic, "test"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), data) {
		t.Fatal("download stdout contains changed bytes or a transfer summary")
	}
	assertStreamProgress(t, diagnostic.Bytes())
}

func assertStreamProgress(t *testing.T, raw []byte) {
	t.Helper()
	if len(raw) == 0 {
		t.Fatal("progress was discarded")
	}
	for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte{'\n'}) {
		var event struct {
			Event string `json:"event"`
			Bytes int64  `json:"bytes"`
		}
		if err := json.Unmarshal(line, &event); err != nil || event.Event != "transfer_progress" || event.Bytes <= 0 {
			t.Fatalf("unexpected progress record %s: %v", line, err)
		}
	}
}

func TestStreamCLIRejectsJSONBeforeIO(t *testing.T) {
	for _, args := range [][]string{
		{"--json", "download", "@missing-file", "remote", "-"},
		{"--json", "proxy", "@missing-file", "--to", "127.0.0.1:80"},
	} {
		var out, diagnostic bytes.Buffer
		err := Run(context.Background(), args, nil, &out, &diagnostic, "test")
		if err == nil || out.Len() != 0 || !json.Valid(bytes.TrimSpace(diagnostic.Bytes())) {
			t.Fatalf("binary JSON rejection: err=%v stdout=%q stderr=%q", err, &out, &diagnostic)
		}
		if !strings.Contains(err.Error(), "bytes") || ExitCode(err) != 2 {
			t.Fatalf("stream validation did not precede ticket I/O or was not a usage error: %v (%s)", err, &diagnostic)
		}
	}
}

func TestMCPRejectsTransferStreamsBeforeDispatch(t *testing.T) {
	calls := make(chan struct{}, 1)
	runner := func(context.Context, []string, io.Reader, io.Writer, io.Writer, string) error {
		calls <- struct{}{}
		return nil
	}
	c := startMCPTest(t, runner, mcpTestOptions())
	for _, direction := range []string{"upload", "download"} {
		paths := []string{"-", "remote"}
		if direction == "download" {
			paths = []string{"remote", "-"}
		}
		c.send(t, mcpCallRequest(direction, "shellpier_"+direction, map[string]any{"ticket": "@missing-file", "positionals": paths}))
		result := mcpResultObject(t, c.read(t))
		encoded, _ := json.Marshal(result)
		if result["isError"] != true || !bytes.Contains(encoded, []byte("JSON-RPC")) {
			t.Fatalf("MCP pipe mode accepted: %s", encoded)
		}
	}
	select {
	case <-calls:
		t.Fatal("a rejected pipe transfer was dispatched")
	default:
	}
}
