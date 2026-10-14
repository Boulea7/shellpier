// Package access manages scoped SSH credentials and pinned client connections.
package access

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const maxTicket = 32 * 1024

var safeID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

// ErrFileChanged reports replacement between a bounded file check and open.
var ErrFileChanged = errors.New("file changed while opening")

type Ticket struct {
	Version        int       `json:"version"`
	Kind           string    `json:"kind"`
	ID             string    `json:"id"`
	RelayURL       string    `json:"relay_url,omitempty"`
	RelayToken     string    `json:"relay_token,omitempty"`
	Addresses      []string  `json:"addresses,omitempty"`
	ExpiresAt      time.Time `json:"expires_at"`
	HostKey        string    `json:"host_key,omitempty"`
	ClientKey      string    `json:"client_key,omitempty"`
	PrivateKey     string    `json:"private_key,omitempty"`
	HostPrivateKey string    `json:"host_private_key,omitempty"`
}

func keypair() (string, string, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, "shellpier")
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))), string(pem.EncodeToMemory(block)), nil
}

// NewPair returns separate credentials for the operator and the target host.
func NewPair(ttl time.Duration) (Ticket, Ticket, error) {
	if ttl < 0 {
		return Ticket{}, Ticket{}, errors.New("TTL must not be negative")
	}
	hp, hpriv, err := keypair()
	if err != nil {
		return Ticket{}, Ticket{}, err
	}
	cp, cpriv, err := keypair()
	if err != nil {
		return Ticket{}, Ticket{}, err
	}
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return Ticket{}, Ticket{}, err
	}
	id := base64.RawURLEncoding.EncodeToString(b)
	var expiry time.Time
	if ttl > 0 {
		expiry = time.Now().Add(ttl).UTC()
	}
	a := Ticket{Version: 1, Kind: "access", ID: id, ExpiresAt: expiry, HostKey: hp, PrivateKey: cpriv}
	h := Ticket{Version: 1, Kind: "host", ID: id, ExpiresAt: expiry, ClientKey: cp, HostPrivateKey: hpriv}
	return a, h, nil
}

func publicKey(s string) (ssh.PublicKey, error) {
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(s))
	if err != nil || comment != "" || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid plain SSH public key")
	}
	if key.Type() != ssh.KeyAlgoED25519 {
		return nil, errors.New("only Ed25519 keys are supported")
	}
	return key, nil
}
func privateKey(s string) (ssh.Signer, error) {
	block, rest := pem.Decode([]byte(s))
	if block == nil || block.Type != "OPENSSH PRIVATE KEY" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("invalid SSH private key envelope")
	}
	signer, err := ssh.ParsePrivateKey([]byte(s))
	if err != nil {
		return nil, errors.New("invalid SSH private key")
	}
	if signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		return nil, errors.New("only Ed25519 keys are supported")
	}
	return signer, nil
}
func validateRelay(value string) error {
	u, err := url.Parse(value)
	if err != nil {
		return errors.New("invalid relay URL")
	}
	if u.User != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errors.New("relay URL must not contain credentials, query, or fragment")
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback()))) {
		return errors.New("relay requires HTTPS except loopback HTTP")
	}
	if strings.ContainsAny(u.Hostname(), " \t\r\n") || u.Hostname() == "" {
		return errors.New("invalid relay host")
	}
	if u.Port() != "" {
		p, e := strconv.Atoi(u.Port())
		if e != nil || p < 1 || p > 65535 {
			return errors.New("invalid relay port")
		}
	}
	return nil
}
func validateAddress(value string) error {
	if len(value) > 512 {
		return errors.New("endpoint exceeds size limit")
	}
	host, port, err := net.SplitHostPort(value)
	if err != nil || host == "" || strings.ContainsAny(host, " /\\\t\r\n@%\"'") {
		return errors.New("invalid direct endpoint")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return errors.New("invalid endpoint port")
	}
	if net.ParseIP(host) == nil {
		if len(host) > 253 {
			return errors.New("invalid endpoint hostname")
		}
		for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
			if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
				return errors.New("invalid endpoint hostname")
			}
			for _, c := range label {
				if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
					return errors.New("invalid endpoint hostname")
				}
			}
		}
	}
	return nil
}

// Validate checks role separation and credentials, permitting an unfinished route.
func (t Ticket) Validate(kind string) error {
	if len(t.RelayURL) > 2048 || len(t.HostKey) > 256 || len(t.ClientKey) > 256 || len(t.PrivateKey) > 4096 || len(t.HostPrivateKey) > 4096 {
		return errors.New("ticket field exceeds size limit")
	}
	if t.Version != 1 {
		return errors.New("unsupported ticket version")
	}
	if t.Kind != "access" && t.Kind != "host" || kind != "" && t.Kind != kind {
		return errors.New("unexpected ticket role")
	}
	if !safeID.MatchString(t.ID) {
		return errors.New("invalid ticket ID")
	}
	if !t.ExpiresAt.IsZero() && !time.Now().Before(t.ExpiresAt) {
		return errors.New("ticket has expired")
	}
	if len(t.Addresses) > 32 {
		return errors.New("too many direct endpoints")
	}
	for _, addr := range t.Addresses {
		if err := validateAddress(addr); err != nil {
			return err
		}
	}
	if t.RelayURL != "" {
		if err := validateRelay(t.RelayURL); err != nil {
			return err
		}
		if !safeID.MatchString(t.RelayToken) {
			return errors.New("invalid relay token")
		}
	} else if t.RelayToken != "" {
		return errors.New("relay token without URL")
	}
	if t.Kind == "access" {
		if t.ClientKey != "" || t.HostPrivateKey != "" {
			return errors.New("host credentials in access ticket")
		}
		if _, err := publicKey(t.HostKey); err != nil {
			return err
		}
		if _, err := privateKey(t.PrivateKey); err != nil {
			return err
		}
	} else {
		if t.HostKey != "" || t.PrivateKey != "" || len(t.Addresses) != 0 {
			return errors.New("access credentials or direct endpoints in host ticket")
		}
		if _, err := publicKey(t.ClientKey); err != nil {
			return err
		}
		if _, err := privateKey(t.HostPrivateKey); err != nil {
			return err
		}
	}
	return nil
}
func (t Ticket) configured() error {
	if err := t.Validate(""); err != nil {
		return err
	}
	if t.RelayURL == "" && (t.Kind == "host" || len(t.Addresses) == 0) {
		return errors.New("ticket has no connection route")
	}
	return nil
}
func (t Ticket) Encode() (string, error) {
	if err := t.configured(); err != nil {
		return "", err
	}
	b, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	token := "sp1_" + base64.RawURLEncoding.EncodeToString(b)
	if len(token) > maxTicket {
		return "", errors.New("ticket exceeds 32 KiB")
	}
	return token, nil
}
func decodeJSON(b []byte) (Ticket, error) {
	var t Ticket
	if len(b) > maxTicket {
		return t, errors.New("ticket exceeds 32 KiB")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&t); err != nil {
		return Ticket{}, errors.New("invalid ticket JSON")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return Ticket{}, errors.New("trailing ticket data")
	}
	if err := t.configured(); err != nil {
		return Ticket{}, err
	}
	return t, nil
}
func Parse(token string) (Ticket, error) {
	if len(token) > maxTicket || !strings.HasPrefix(token, "sp1_") {
		return Ticket{}, errors.New("invalid ticket prefix or length")
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimPrefix(token, "sp1_"))
	if err != nil {
		return Ticket{}, errors.New("invalid ticket encoding")
	}
	return decodeJSON(b)
}

// Read accepts a token, @file, or a regular file containing a token or JSON.
func Read(input string) (Ticket, error) {
	if strings.HasPrefix(input, "sp1_") {
		return Parse(input)
	}
	path := strings.TrimPrefix(input, "@")
	b, err := ReadFile(path, maxTicket)
	if err != nil {
		return Ticket{}, err
	}
	b = bytes.TrimSpace(b)
	if bytes.HasPrefix(b, []byte("sp1_")) {
		return Parse(string(b))
	}
	return decodeJSON(b)
}

// ReadFile reads a bounded regular file without following a final symlink.
// Unix opens are nonblocking so a replacement FIFO cannot hang the caller.
func ReadFile(path string, limit int64) ([]byte, error) {
	if limit < 0 || limit == int64(^uint64(0)>>1) {
		return nil, errors.New("invalid file size limit")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("file must be regular and within size limit")
	}
	f, err := openTicket(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, ErrFileChanged
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("file exceeds size limit")
	}
	return b, nil
}
