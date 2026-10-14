package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
)

func TestEveryCommandHelp(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("SHELLPIER_RELAY_URL", "http://127.0.0.1:1")
	t.Setenv("SHELLPIER_RELAY_KEY", "invalid")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, command := range strings.Fields(completionCommands) {
		for _, option := range []string{"--help", "-h"} {
			t.Run(command+"/"+option, func(t *testing.T) {
				var out bytes.Buffer
				if err := Run(ctx, []string{command, option}, nil, &out, &out, "test"); err != nil {
					t.Fatal(err)
				}
				if out.Len() == 0 {
					t.Fatal("help returned no usage text")
				}
			})
		}
	}
	entries, err := os.ReadDir(".")
	if err != nil || len(entries) != 0 {
		t.Fatalf("help created files: %v, %v", entries, err)
	}
}

func TestRevokeTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	_, host, err := access.NewPair(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	host.RelayURL, host.RelayToken = server.URL, strings.Repeat("t", 64)
	file := filepath.Join(t.TempDir(), "host.ticket")
	if err := writeJSONPrivate(file, host); err != nil {
		t.Fatal(err)
	}
	err = Run(context.Background(), []string{"revoke", "@" + file, "--timeout", "1s"}, nil, io.Discard, io.Discard, "test")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("revocation did not honor its timeout: %v", err)
	}
	for _, duration := range []string{"0", "-1s", "500ms", "10m"} {
		err := Run(context.Background(), []string{"revoke", "--timeout", duration, "@missing"}, nil, io.Discard, io.Discard, "test")
		if err == nil || !strings.Contains(err.Error(), "timeout must be") {
			t.Fatalf("timeout %q was not rejected before file access: %v", duration, err)
		}
	}
}
