package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/platform"
	"github.com/Boulea7/shellpier/internal/relay"
)

func flags(name string, stderr io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(stderr)
	return f
}

func tokenKey(path string) (string, error) {
	value := os.Getenv("SHELLPIER_RELAY_KEY")
	if path != "" {
		b, err := readRegular(path, 4096)
		if err != nil {
			return "", err
		}
		value = strings.TrimSpace(string(b))
	}
	if len(value) < 32 || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("relay key needs at least 32 characters; set SHELLPIER_RELAY_KEY or --token-file")
	}
	return value, nil
}

func secret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func readRegular(path string, limit int64) ([]byte, error) {
	return access.ReadFile(path, limit)
}

func displayArg(value string) string {
	if runtime.GOOS == "windows" {
		return "'" + strings.ReplaceAll(value, "'", "''") + "'"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func writePrivate(path string, contents []byte) (result error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	created, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	defer func() {
		result = errors.Join(result, f.Close())
		if result != nil {
			if current, err := os.Lstat(path); err == nil && os.SameFile(created, current) {
				result = errors.Join(result, os.Remove(path))
			}
		}
	}()
	if err = platform.SecureFile(path); err != nil {
		return err
	}
	_, err = f.Write(contents)
	return err
}

func writeJSONPrivate(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(b, '\n'))
}

func ticketSession(t access.Ticket) relay.Session {
	s := relay.Session{ID: t.ID, ExpiresAt: t.ExpiresAt}
	if t.Kind == "host" {
		s.HostToken = t.RelayToken
	} else {
		s.ClientToken = t.RelayToken
	}
	return s
}
