package access

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"

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

// SSHConfigOptions controls generated OpenSSH settings. Durations use whole seconds.
type SSHConfigOptions struct {
	ServerAliveInterval time.Duration
	ServerAliveCountMax int
	ControlPersist      time.Duration
}

func DefaultSSHConfigOptions() SSHConfigOptions {
	return SSHConfigOptions{ServerAliveInterval: 15 * time.Second, ServerAliveCountMax: 3}
}

// Validate checks options and the destination path without creating files.
func (options SSHConfigOptions) Validate(dir string) error {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	_, err = configOptions(options, absolute, runtime.GOOS)
	return err
}

func configOptions(options SSHConfigOptions, dir, goos string) (string, error) {
	if options.ServerAliveInterval < 0 || options.ServerAliveInterval%time.Second != 0 {
		return "", errors.New("ServerAliveInterval must be a nonnegative whole number of seconds")
	}
	if options.ServerAliveCountMax < 0 || int64(options.ServerAliveCountMax) > 2147483647 || options.ServerAliveInterval/time.Second > 2147483647 {
		return "", errors.New("ServerAlive settings exceed OpenSSH integer bounds")
	}
	extra := fmt.Sprintf("  ServerAliveInterval %d\n  ServerAliveCountMax %d\n", options.ServerAliveInterval/time.Second, options.ServerAliveCountMax)
	if options.ControlPersist == 0 {
		return extra, nil
	}
	if goos == "windows" {
		return "", errors.New("ControlPersist is supported only on Unix")
	}
	if options.ControlPersist < time.Second || options.ControlPersist > time.Hour || options.ControlPersist%time.Second != 0 {
		return "", errors.New("ControlPersist must be whole seconds between 1s and 1h")
	}
	path := filepath.Join(dir, "control.sock")
	// Leave room for OpenSSH's temporary socket suffix within sockaddr_un.sun_path.
	if len(path) > 80 {
		return "", errors.New("ControlPath is too long (maximum 80 bytes)")
	}
	if strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return "", errors.New("ControlPath contains a control character")
	}
	quoted, err := configQuote(path)
	if err != nil {
		return "", err
	}
	return extra + fmt.Sprintf("  ControlMaster auto\n  ControlPersist %d\n  ControlPath %s\n", options.ControlPersist/time.Second, quoted), nil
}

// WriteConfig creates a new private directory, leaving global SSH files untouched.
func WriteConfig(t Ticket, dir, binaryPath string) (string, error) {
	return WriteConfigWithOptions(t, dir, binaryPath, DefaultSSHConfigOptions())
}

// WriteConfigWithOptions creates an isolated configuration without starting an SSH master.
func WriteConfigWithOptions(t Ticket, dir, binaryPath string, options SSHConfigOptions) (string, error) {
	return writeConfigOptions(t, dir, binaryPath, options, platform.SecureFile)
}
func writeConfig(t Ticket, dir, binaryPath string, secure func(string) error) (string, error) {
	return writeConfigOptions(t, dir, binaryPath, DefaultSSHConfigOptions(), secure)
}
func writeConfigOptions(t Ticket, dir, binaryPath string, options SSHConfigOptions, secure func(string) error) (configPath string, resultErr error) {
	if _, err := clientConfig(t); err != nil {
		return "", err
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	extra, err := configOptions(options, dir, runtime.GOOS)
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
	config := fmt.Sprintf("Host shellpier\n  HostName shellpier\n  User shellpier\n  HostKeyAlias %s\n  IdentityFile %s\n  UserKnownHostsFile %s\n  GlobalKnownHostsFile none\n  ProxyCommand %s\n  StrictHostKeyChecking yes\n  IdentityAgent none\n  IdentitiesOnly yes\n  BatchMode yes\n  RequestTTY no\n", alias, identity, known, proxy) + extra
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
