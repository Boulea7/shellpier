package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/platform"
)

func TestDoctorTargetStagesRealSSH(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	dir := t.TempDir()
	ticket := filepath.Join(dir, "ticket.json")
	state, done := startHost(t, "share", "--listen", "127.0.0.1:0", "--ttl", "0", "--output", ticket, "--json")
	var out bytes.Buffer
	if err := doctor(context.Background(), []string{"--target", "@" + ticket, "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	var report struct {
		Stages []doctorStage `json:"target_stages"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil || len(report.Stages) != 4 {
		t.Fatalf("stages: %s %v", &out, err)
	}
	for _, stage := range report.Stages {
		if !stage.OK {
			t.Fatalf("stage failed: %+v", stage)
		}
	}
	if strings.Contains(out.String(), "private_key") || strings.Contains(out.String(), "relay_token") {
		t.Fatal("diagnostic leaked credential")
	}
	invoke(t, "stop", "--state", state)
	awaitHost(t, done, state)
}
func TestDoctorHealthDoesNotProveSSH(t *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"ok":true}`) }))
	defer health.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	ticket, _, err := access.NewPair(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ticket.Addresses = []string{address}
	file := filepath.Join(t.TempDir(), "access.json")
	data, _ := json.Marshal(ticket)
	if err = os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	platform.SecureFile(file)
	var out bytes.Buffer
	err = doctor(context.Background(), []string{"--relay", health.URL, "--target", "@" + file, "--retries=0", "--json"}, &out, io.Discard)
	if err == nil || !strings.Contains(out.String(), `"stage": "ssh"`) || !strings.Contains(out.String(), "HTTPS health OK") {
		t.Fatalf("health hid SSH failure: %s %v", &out, err)
	}
}
func TestDoctorTargetTicketFailureAndHelp(t *testing.T) {
	t.Setenv("SHELLPIER_RELAY_URL", "")
	var out bytes.Buffer
	err := doctor(context.Background(), []string{"--target", "@missing", "--json"}, &out, io.Discard)
	if err == nil || !strings.Contains(out.String(), `"stage": "ticket"`) {
		t.Fatalf("ticket failure: %s %v", &out, err)
	}
	out.Reset()
	err = doctor(context.Background(), []string{"--help", "--target", "@missing"}, &out, io.Discard)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help attempted access: %v", err)
	}
}
