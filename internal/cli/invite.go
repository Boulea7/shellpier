package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/relay"
)

func invite(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	f := flags("invite", stderr)
	base := f.String("relay", os.Getenv("SHELLPIER_RELAY_URL"), "HTTPS relay URL")
	keyFile := f.String("token-file", "", "relay enrollment key file")
	ttl := f.Duration("ttl", time.Hour, "authorization lifetime; 0 means explicitly persistent")
	dir := f.String("out", "", "new private output directory")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || *base == "" || *dir == "" || *ttl < 0 || *ttl > 0 && *ttl < time.Second {
		return errors.New("usage: shellpier invite --relay URL --out NEW-DIRECTORY [--ttl 1h]")
	}
	key, err := tokenKey(*keyFile)
	if err != nil {
		return err
	}
	a, h, err := access.NewPair(*ttl)
	if err != nil {
		return err
	}
	createCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	s, err := relay.Create(createCtx, *base, key, *ttl)
	cancel()
	if err != nil {
		return err
	}
	a.ID, h.ID = s.ID, s.ID
	a.ExpiresAt, h.ExpiresAt = s.ExpiresAt, s.ExpiresAt
	a.RelayURL, h.RelayURL = *base, *base
	a.RelayToken, h.RelayToken = s.ClientToken, s.HostToken
	success := false
	defer func() {
		if !success {
			c, done := context.WithTimeout(context.Background(), 5*time.Second)
			defer done()
			if e := relay.Revoke(c, *base, s); e != nil {
				fmt.Fprintln(stderr, "Failed to revoke incomplete invitation:", e)
			}
		}
	}()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	config, err := access.WriteConfig(a, *dir, exe)
	if err != nil {
		return err
	}
	hostPath := filepath.Join(filepath.Dir(config), "host.ticket")
	if err := writeJSONPrivate(hostPath, h); err != nil {
		return err
	}
	// The host ticket is deliberately separate from the operator's private key.
	fmt.Fprintf(stdout, "Transfer host.ticket privately to the target, then run:\n  PowerShell: .\\shellpier.exe host '.\\host.ticket'\n  CMD:        shellpier.exe host host.ticket\n  Linux/mac:  ./shellpier host ./host.ticket\n\nHost ticket: %s\nOn this computer: ssh -F %s shellpier whoami\n", hostPath, displayArg(config))
	success = true
	return nil
}

func revoke(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	input, args := ticketArgument(args)
	f := flags("revoke", stderr)
	timeout := f.Duration("timeout", 10*time.Second, "total revocation timeout")
	if err := f.Parse(args); err != nil {
		return err
	}
	if input == "" && f.NArg() == 1 {
		input = f.Arg(0)
	} else if f.NArg() != 0 {
		return errors.New("revoke accepts exactly one host ticket")
	}
	if input == "" || *timeout < time.Second || *timeout > 5*time.Minute {
		return errors.New("usage: shellpier revoke HOST-TICKET [--timeout 10s]; timeout must be 1s to 5m")
	}
	t, err := access.Read(input)
	if err != nil {
		return err
	}
	if err := t.Validate("host"); err != nil {
		return err
	}
	c, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	if err := relay.Revoke(c, t.RelayURL, ticketSession(t)); err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, "Session revoked; relay connections closed.")
	return err
}
