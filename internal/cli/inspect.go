package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/Boulea7/shellpier/internal/access"
	"golang.org/x/crypto/ssh"
)

const inspectionLimit = 32 * 1024

type ticketSummary struct {
	Kind          string    `json:"kind"`
	ID            string    `json:"id"`
	Transport     string    `json:"transport"`
	Addresses     []string  `json:"addresses,omitempty"`
	ExpiresAt     time.Time `json:"expires_at"`
	Persistent    bool      `json:"persistent"`
	Expired       bool      `json:"expired"`
	HostKeySHA256 string    `json:"host_key_sha256"`
}

// readInspectable preserves expiry for display while validating all credentials.
func readInspectable(input string) (access.Ticket, error) {
	var b []byte
	var err error
	if strings.HasPrefix(input, "target:") {
		input, err = access.ResolveTarget("", strings.TrimPrefix(input, "target:"))
		if err != nil {
			return access.Ticket{}, err
		}
	}
	if strings.HasPrefix(input, "sp1_") {
		if len(input) > inspectionLimit {
			return access.Ticket{}, errors.New("ticket exceeds 32 KiB")
		}
		b = []byte(input)
	} else {
		b, err = access.ReadFile(strings.TrimPrefix(input, "@"), inspectionLimit)
		if err != nil {
			return access.Ticket{}, errors.New("cannot read a bounded regular ticket file")
		}
	}
	b = bytes.TrimSpace(b)
	if len(b) > inspectionLimit {
		return access.Ticket{}, errors.New("ticket exceeds 32 KiB")
	}
	if bytes.HasPrefix(b, []byte("sp1_")) {
		b, err = base64.RawURLEncoding.Strict().DecodeString(string(b[4:]))
		if err != nil {
			return access.Ticket{}, errors.New("invalid ticket encoding")
		}
	}
	var t access.Ticket
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&t) != nil {
		return t, errors.New("invalid ticket JSON")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return access.Ticket{}, errors.New("trailing ticket data")
	}
	expiry := t.ExpiresAt
	t.ExpiresAt = time.Time{}
	if err = t.Validate(""); err != nil {
		return access.Ticket{}, err
	}
	if t.RelayURL == "" && (t.Kind == "host" || len(t.Addresses) == 0) {
		return access.Ticket{}, errors.New("ticket has no connection route")
	}
	t.ExpiresAt = expiry
	return t, nil
}
func summarizeTicket(t access.Ticket) (ticketSummary, error) {
	var key ssh.PublicKey
	if t.Kind == "host" {
		signer, e := ssh.ParsePrivateKey([]byte(t.HostPrivateKey))
		if e != nil {
			return ticketSummary{}, errors.New("invalid host key")
		}
		key = signer.PublicKey()
	} else {
		var e error
		key, _, _, _, e = ssh.ParseAuthorizedKey([]byte(t.HostKey))
		if e != nil {
			return ticketSummary{}, errors.New("invalid host key")
		}
	}
	transport := "direct"
	if t.RelayURL != "" {
		transport = "relay"
		if len(t.Addresses) > 0 {
			transport = "direct+relay"
		}
	}
	return ticketSummary{Kind: t.Kind, ID: t.ID, Transport: transport, Addresses: t.Addresses, ExpiresAt: t.ExpiresAt, Persistent: t.ExpiresAt.IsZero(), Expired: !t.ExpiresAt.IsZero() && !time.Now().Before(t.ExpiresAt), HostKeySHA256: ssh.FingerprintSHA256(key)}, nil
}
func inspectionJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	return encoder.Encode(value)
}

func inspect(args []string, stdout, stderr io.Writer) error {
	input, args := ticketArgument(args)
	f := flags("inspect", stderr)
	asJSON := f.Bool("json", false, "print a credential-free ticket summary")
	if e := parseFlags(f, args); e != nil {
		return e
	}
	if input == "" && f.NArg() == 1 {
		input = f.Arg(0)
	} else if f.NArg() != 0 {
		return errors.New("inspect accepts exactly one ticket")
	}
	if input == "" {
		return errors.New("usage: shellpier inspect TICKET [--json]")
	}
	ticket, e := readInspectable(input)
	if e != nil {
		return e
	}
	summary, e := summarizeTicket(ticket)
	if e != nil {
		return e
	}
	if *asJSON {
		return inspectionJSON(stdout, summary)
	}
	expiry := "persistent"
	if !summary.Persistent {
		expiry = summary.ExpiresAt.Format(time.RFC3339)
		if summary.Expired {
			expiry += " (expired)"
		}
	}
	_, e = fmt.Fprintf(stdout, "Kind: %s\nSession: %s\nTransport: %s\nExpires: %s\nHost key: %s\n", summary.Kind, summary.ID, summary.Transport, expiry, summary.HostKeySHA256)
	if e != nil {
		return e
	}
	for _, address := range summary.Addresses {
		if _, e = fmt.Fprintln(stdout, "Address:", address); e != nil {
			return e
		}
	}
	return nil
}

type checkResult struct {
	OK               bool   `json:"ok"`
	IdentityVerified bool   `json:"identity_verified"`
	AccessMode       string `json:"access_mode,omitempty"`
	ID               string `json:"id,omitempty"`
	Hostname         string `json:"hostname,omitempty"`
	User             string `json:"user,omitempty"`
	HostKeySHA256    string `json:"host_key_sha256,omitempty"`
	Error            string `json:"error,omitempty"`
}
type identityOutput struct{ bytes.Buffer }

func (w *identityOutput) Write(p []byte) (int, error) {
	if w.Len()+len(p) > 4096 {
		return 0, errors.New("identity output exceeds limit")
	}
	return w.Buffer.Write(p)
}
func readIdentity(client *ssh.Client, command string) (string, error) {
	session, e := client.NewSession()
	if e != nil {
		return "", e
	}
	defer session.Close()
	var out identityOutput
	session.Stdout = &out
	session.Stderr = io.Discard
	if e = access.ConnectionError(client, session.Run(command)); e != nil {
		return "", e
	}
	value := strings.TrimSpace(out.String())
	if value == "" || len(value) > 1024 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "", errors.New("invalid identity response")
	}
	return value, nil
}
func check(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	input, args := ticketArgument(args)
	f := flags("check", stderr)
	retry := clientRetryFlags(f)
	asJSON := f.Bool("json", false, "print SSH and identity check results")
	timeout := f.Duration("timeout", 20*time.Second, "total SSH and identity check timeout")
	iface := f.String("interface", "", "constrain direct TCP routing to this interface")
	if e := parseFlags(f, args); e != nil {
		return e
	}
	if input == "" && f.NArg() == 1 {
		input = f.Arg(0)
	} else if f.NArg() != 0 {
		return errors.New("check accepts exactly one access ticket")
	}
	if input == "" || *timeout < time.Second || *timeout > 5*time.Minute {
		return errors.New("usage: shellpier check TICKET [--timeout 20s] [--interface NAME] [--json]")
	}
	if err := retry.Validate(); err != nil {
		return err
	}
	ctx = retry.Context(ctx)
	result := checkResult{}
	fail := func(message string, cause ...error) error {
		result.Error = message
		if *asJSON {
			if e := inspectionJSON(stdout, result); e != nil {
				return e
			}
		}
		if len(cause) != 0 && cause[0] != nil {
			return fmt.Errorf("%s: %w", message, cause[0])
		}
		return errors.New(message)
	}
	ticket, e := access.Read(input)
	if e != nil {
		return fail("invalid, expired, or unreadable access ticket", e)
	}
	if ticket.Validate("access") != nil {
		return fail("requires a valid access ticket")
	}
	result.ID = ticket.ID
	summary, e := summarizeTicket(ticket)
	if e != nil {
		return fail("invalid host key")
	}
	result.HostKeySHA256 = summary.HostKeySHA256
	runCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	client, e := access.DialRetry(runCtx, ticket, *iface, retry.RetryOptions)
	if e != nil {
		if runCtx.Err() != nil {
			return fail("SSH check timed out or was canceled", runCtx.Err())
		}
		return fail("SSH check failed", e)
	}
	defer client.Close()
	caps, supported, e := queryRemoteCapabilities(client)
	if e != nil {
		return fail("remote capability check failed", e)
	}
	result.AccessMode = caps.AccessMode
	if supported && caps.AccessMode != "full" {
		result.OK = true
		if *asJSON {
			return inspectionJSON(stdout, result)
		}
		_, e = fmt.Fprintf(stdout, "SSH authenticated.\nAccess mode: %s\nHost and user commands were not run; SFTP availability is advertised by the target.\nHost key: %s\n", result.AccessMode, result.HostKeySHA256)
		return e
	}
	result.Hostname, e = readIdentity(client, "hostname")
	if e != nil {
		if runCtx.Err() != nil {
			return fail("SSH check timed out or was canceled", runCtx.Err())
		}
		return fail("remote hostname check failed", e)
	}
	result.User, e = readIdentity(client, "whoami")
	if e != nil {
		if runCtx.Err() != nil {
			return fail("SSH check timed out or was canceled", runCtx.Err())
		}
		return fail("remote account check failed", e)
	}
	result.OK = true
	result.IdentityVerified = true
	if *asJSON {
		return inspectionJSON(stdout, result)
	}
	_, e = fmt.Fprintf(stdout, "SSH verified.\nHost: %s\nUser: %s\nHost key: %s\n", result.Hostname, result.User, result.HostKeySHA256)
	return e
}
