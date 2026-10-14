package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/sshd"
	"golang.org/x/crypto/ssh"
)

func inspectionFixture(t *testing.T) (access.Ticket, access.Ticket) {
	t.Helper()
	a, h, e := access.NewPair(time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	a.Addresses = []string{"127.0.0.1:2222"}
	h.RelayURL = "https://relay.example.com"
	h.RelayToken = strings.Repeat("r", 43)
	return a, h
}
func inspectJSONTicket(t *testing.T, ticket access.Ticket) string {
	t.Helper()
	b, e := json.Marshal(ticket)
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "ticket.json")
	if e = os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	return "@" + path
}
func assertInspectionRedacted(t *testing.T, output string, a, h access.Ticket) {
	t.Helper()
	for _, secret := range []string{a.PrivateKey, h.HostPrivateKey, h.RelayToken, a.HostKey, h.ClientKey} {
		if secret != "" && strings.Contains(output, secret) {
			t.Fatal("output disclosed credential")
		}
	}
	if strings.Contains(output, "BEGIN OPENSSH PRIVATE KEY") {
		t.Fatal("output disclosed private key")
	}
}
func TestInspectOfflineExpiredAndRedacted(t *testing.T) {
	a, h := inspectionFixture(t)
	for _, ticket := range []access.Ticket{a, h} {
		ticket.ExpiresAt = time.Now().Add(-time.Hour)
		var out, stderr bytes.Buffer
		if e := inspect([]string{inspectJSONTicket(t, ticket), "--json"}, &out, &stderr); e != nil {
			t.Fatal(e)
		}
		var summary ticketSummary
		if e := json.Unmarshal(out.Bytes(), &summary); e != nil {
			t.Fatal(e)
		}
		if !summary.Expired || summary.Kind != ticket.Kind || summary.ID != ticket.ID || !strings.HasPrefix(summary.HostKeySHA256, "SHA256:") {
			t.Fatal("summary incorrect")
		}
		assertInspectionRedacted(t, out.String()+stderr.String(), a, h)
	}
	a.ExpiresAt = time.Time{}
	token, e := a.Encode()
	if e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	if e = inspect([]string{"--json", token}, &out, &bytes.Buffer{}); e != nil {
		t.Fatal(e)
	}
	var summary ticketSummary
	json.Unmarshal(out.Bytes(), &summary)
	if !summary.Persistent || summary.Expired {
		t.Fatal("persistent summary incorrect")
	}
}
func TestInspectMalformed(t *testing.T) {
	a, _ := inspectionFixture(t)
	a.ExpiresAt = time.Now().Add(-time.Hour)
	a.HostKey = "invalid"
	for _, input := range []string{inspectJSONTicket(t, a), "sp1_invalid=", strings.Repeat("x", inspectionLimit+1)} {
		if e := inspect([]string{input}, &bytes.Buffer{}, &bytes.Buffer{}); e == nil {
			t.Fatal("invalid input accepted")
		}
	}
	// Expiry alone is allowed; role mixing is not.
	a, _ = inspectionFixture(t)
	a.HostPrivateKey = a.PrivateKey
	b, _ := json.Marshal(a)
	token := "sp1_" + base64.RawURLEncoding.EncodeToString(b)
	if e := inspect([]string{token}, &bytes.Buffer{}, &bytes.Buffer{}); e == nil {
		t.Fatal("role mix accepted")
	}
}
func inspectionServer(t *testing.T, h access.Ticket) string {
	t.Helper()
	signer, e := ssh.ParsePrivateKey([]byte(h.HostPrivateKey))
	if e != nil {
		t.Fatal(e)
	}
	key, _, _, _, e := ssh.ParseAuthorizedKey([]byte(h.ClientKey))
	if e != nil {
		t.Fatal(e)
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); ln.Close() })
	srv := &sshd.Server{Signer: signer, AuthorizedKey: key, Shell: "auto", WorkDir: t.TempDir()}
	go func() {
		for {
			conn, e := ln.Accept()
			if e != nil {
				return
			}
			go func() { _ = srv.ServeConn(ctx, conn) }()
		}
	}()
	return ln.Addr().String()
}
func TestCheckActualSSHIdentity(t *testing.T) {
	a, h := inspectionFixture(t)
	a.Addresses = []string{inspectionServer(t, h)}
	token, e := a.Encode()
	if e != nil {
		t.Fatal(e)
	}
	var out, stderr bytes.Buffer
	if e = check(context.Background(), []string{token, "--json"}, &out, &stderr); e != nil {
		t.Fatal(e)
	}
	var result checkResult
	if e = json.Unmarshal(out.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if !result.OK || result.Hostname == "" || result.User == "" || result.ID != a.ID {
		t.Fatal("identity check incomplete")
	}
	assertInspectionRedacted(t, out.String()+stderr.String(), a, h)
	other, _ := inspectionFixture(t)
	a.HostKey = other.HostKey
	token, _ = a.Encode()
	out.Reset()
	if e = check(context.Background(), []string{token, "--json"}, &out, &stderr); e == nil {
		t.Fatal("wrong host pin accepted")
	}
	json.Unmarshal(out.Bytes(), &result)
	if result.OK || result.Error == "" {
		t.Fatal("failure JSON missing")
	}
	assertInspectionRedacted(t, out.String()+stderr.String(), a, h)
}
func TestCheckCancelAndExpired(t *testing.T) {
	a, h := inspectionFixture(t)
	a.Addresses = []string{inspectionServer(t, h)}
	token, _ := a.Encode()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if e := check(ctx, []string{token, "--json"}, &out, &bytes.Buffer{}); e == nil {
		t.Fatal("cancellation accepted")
	}
	var result checkResult
	if json.Unmarshal(out.Bytes(), &result) != nil || result.OK || !strings.Contains(result.Error, "canceled") {
		t.Fatal("cancellation JSON missing")
	}
	a.ExpiresAt = time.Now().Add(-time.Hour)
	out.Reset()
	if e := check(context.Background(), []string{inspectJSONTicket(t, a), "--json"}, &out, &bytes.Buffer{}); e == nil {
		t.Fatal("expired check accepted")
	}
}
