package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/platform"
)

func TestTargetsCLI(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "config")
	file := filepath.Join(base, "ticket.json")
	ticket, _, err := access.NewPair(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ticket.Addresses = []string{"127.0.0.1:2222"}
	data, _ := json.Marshal(ticket)
	if err = os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = platform.SecureFile(file); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	run := func(args ...string) error {
		out.Reset()
		return targets(context.Background(), append(args, "--config-dir", dir, "--json"), nil, &out, io.Discard)
	}
	if err = run("add", "dev", "--ticket", file); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), ticket.PrivateKey) || strings.Contains(out.String(), "private_key") {
		t.Fatal("secret in metadata")
	}
	if err = run("show", "dev"); err != nil || !strings.Contains(out.String(), `"status":"active"`) {
		t.Fatalf("show: %s %v", &out, err)
	}
	if err = run("list"); err != nil {
		t.Fatal(err)
	}
	if err = run("remove", "dev"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(file); err != nil {
		t.Fatal("ticket removed")
	}
}
func TestTargetsHelpNoSideEffects(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "must-not-exist")
	for _, action := range []string{"add", "list", "show", "remove"} {
		err := targets(context.Background(), []string{action, "--help", "--config-dir", dir}, nil, io.Discard, io.Discard)
		if !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("help: %v", err)
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("help created config")
	}
}
