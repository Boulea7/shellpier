package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"runtime"
	"testing"
)

func TestVersionOutput(t *testing.T) {
	var out bytes.Buffer
	if err := printVersion(nil, &out, io.Discard, "v1.2.3-test"); err != nil || out.String() != "shellpier v1.2.3-test\n" {
		t.Fatalf("plain version: %q, %v", out.String(), err)
	}
	out.Reset()
	if err := printVersion([]string{"--json"}, &out, io.Discard, "v1.2.3-test"); err != nil {
		t.Fatal(err)
	}
	var info buildVersion
	if err := json.Unmarshal(out.Bytes(), &info); err != nil {
		t.Fatal(err)
	}
	if info.Version != "v1.2.3-test" || info.OS != runtime.GOOS || info.Arch != runtime.GOARCH || info.Go != runtime.Version() {
		t.Fatalf("unexpected metadata: %+v", info)
	}
	for _, args := range [][]string{{"unexpected"}, {"--unknown"}} {
		if err := printVersion(args, io.Discard, io.Discard, "test"); err == nil {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
	}
}
