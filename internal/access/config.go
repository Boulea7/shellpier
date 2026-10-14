package access

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/Boulea7/shellpier/internal/platform"
)

var configFiles = []string{"access.json", "identity", "known_hosts", "ssh_config"}

// configQuote quotes an OpenSSH configuration argument; percent is expanded by SSH.
func configQuote(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("configuration path contains a control character")
	}
	value = strings.ReplaceAll(value, "%", "%%")
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return "\"" + value + "\"", nil
}
func shellQuote(value string, windows bool) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("proxy path contains a control character")
	}
	if windows {
		// CMD expands these characters even inside double quotes. Reject rather than
		// trying to emulate incompatible CMD, PowerShell, and CRT parsing rules.
		if strings.ContainsAny(value, "\"%!^&|<>()") {
			return "", errors.New("Windows proxy paths must not contain quotes, %, !, ^, &, |, <, >, or parentheses")
		}
		if strings.HasSuffix(value, "\\") {
			return "", errors.New("Windows proxy path must name a file")
		}
		return "\"" + value + "\"", nil
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'", nil
}
func proxyCommand(binary, path string, windows bool) (string, error) {
	b, err := shellQuote(binary, windows)
	if err != nil {
		return "", err
	}
	p, err := shellQuote("@"+path, windows)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(b+" proxy "+p, "%", "%%"), nil
}

type ownedFile struct {
	path string
	info os.FileInfo
}

func exclusiveFile(path string, data []byte, secure func(string) error) (*ownedFile, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	owned := &ownedFile{path: path}
	info, err := f.Stat()
	owned.info = info
	if err != nil {
		f.Close()
		return owned, err
	}
	// Establish the Windows ACL before writing any secret bytes.
	if err = secure(path); err != nil {
		f.Close()
		return owned, err
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return owned, err
	}
	return owned, closeErr
}
func rollbackConfig(dir string, files []ownedFile) error {
	var failures []error
	for i := len(files) - 1; i >= 0; i-- {
		file := files[i]
		if file.info == nil {
			failures = append(failures, fmt.Errorf("cannot verify created output %s", filepath.Base(file.path)))
			continue
		}
		current, err := os.Lstat(file.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if !os.SameFile(file.info, current) {
			continue
		}
		if err = os.Remove(file.path); err != nil {
			failures = append(failures, err)
		}
	}
	// Remove only an empty directory; concurrent unrelated additions survive.
	if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
		entries, readErr := os.ReadDir(dir)
		if readErr != nil || len(entries) == 0 {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// WriteConfig creates a new private directory, leaving global SSH files untouched.
func WriteConfig(t Ticket, dir, binaryPath string) (string, error) {
	return writeConfig(t, dir, binaryPath, platform.SecureFile)
}
func writeConfig(t Ticket, dir, binaryPath string, secure func(string) error) (configPath string, resultErr error) {
	if _, err := clientConfig(t); err != nil {
		return "", err
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	binaryPath, err = filepath.Abs(binaryPath)
	if err != nil {
		return "", err
	}
	identity, err := configQuote(filepath.Join(dir, "identity"))
	if err != nil {
		return "", err
	}
	known, err := configQuote(filepath.Join(dir, "known_hosts"))
	if err != nil {
		return "", err
	}
	proxy, err := proxyCommand(binaryPath, filepath.Join(dir, "access.json"), runtime.GOOS == "windows")
	if err != nil {
		return "", err
	}
	alias := "shellpier-" + strings.ToLower(t.ID)
	config := fmt.Sprintf("Host shellpier\n  HostName shellpier\n  User shellpier\n  HostKeyAlias %s\n  IdentityFile %s\n  UserKnownHostsFile %s\n  GlobalKnownHostsFile none\n  ProxyCommand %s\n  StrictHostKeyChecking yes\n  IdentityAgent none\n  IdentitiesOnly yes\n  BatchMode yes\n  RequestTTY no\n", alias, identity, known, proxy)
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return "", err
	}
	manifest, err := json.MarshalIndent(struct {
		Version int      `json:"version"`
		ID      string   `json:"id"`
		Files   []string `json:"files"`
	}{1, t.ID, configFiles}, "", "  ")
	if err != nil {
		return "", err
	}
	if err = os.Mkdir(dir, 0700); err != nil {
		return "", fmt.Errorf("output directory must be new: %w", err)
	}
	var owned []ownedFile
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, rollbackConfig(dir, owned))
		}
	}()
	if err = platform.SecureDir(dir); err != nil {
		return "", err
	}
	contents := [][]byte{append(data, '\n'), []byte(t.PrivateKey), []byte(alias + " " + t.HostKey + "\n"), []byte(config)}
	for i, name := range configFiles {
		path := filepath.Join(dir, name)
		created, writeErr := exclusiveFile(path, contents[i], secure)
		if created != nil {
			owned = append(owned, *created)
		}
		if writeErr != nil {
			return "", writeErr
		}
	}
	path := filepath.Join(dir, "shellpier-manifest.json")
	created, writeErr := exclusiveFile(path, append(manifest, '\n'), secure)
	if created != nil {
		owned = append(owned, *created)
	}
	if writeErr != nil {
		return "", writeErr
	}
	return filepath.Join(dir, "ssh_config"), nil
}
