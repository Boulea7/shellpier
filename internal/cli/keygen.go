package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Boulea7/shellpier/internal/platform"
	"golang.org/x/crypto/ssh"
)

type keygenResult struct {
	PrivateKeyPath string `json:"private_key_path"`
	PublicKeyPath  string `json:"public_key_path"`
	Fingerprint    string `json:"fingerprint"`
}

func keygen(args []string, stdout, stderr io.Writer) error {
	f := flags("keygen", stderr)
	out := f.String("out", "", "new private output directory; parent must exist")
	asJSON := f.Bool("json", false, "emit key paths and SHA256 fingerprint as JSON")
	if err := f.Parse(args); err != nil {
		return err
	}
	if *out == "" || f.NArg() != 0 {
		return errors.New("keygen requires --out NEW-DIRECTORY and accepts no positional arguments")
	}
	result, err := generateKeyPair(*out, platform.SecureFile)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(result)
	}
	_, err = fmt.Fprintf(stdout, "private_key_path: %s\npublic_key_path: %s\nfingerprint: %s\n", result.PrivateKeyPath, result.PublicKeyPath, result.Fingerprint)
	return err
}

type keygenOwnedFile struct {
	path string
	info os.FileInfo
}

func generateKeyPair(out string, secureFile func(string) error) (result keygenResult, resultErr error) {
	dir, err := filepath.Abs(out)
	if err != nil {
		return result, err
	}
	parent, err := os.Stat(filepath.Dir(dir))
	if err != nil {
		return result, fmt.Errorf("output parent must exist: %w", err)
	}
	if !parent.IsDir() {
		return result, errors.New("output parent is not a directory")
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return result, err
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	if err != nil {
		return result, err
	}
	publicKey, err := ssh.NewPublicKey(public)
	if err != nil {
		return result, err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return result, fmt.Errorf("create new key directory: %w", err)
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil {
		return result, err
	}
	owned := []keygenOwnedFile{}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, rollbackKeyPair(dir, dirInfo, owned))
		}
	}()
	if err := platform.SecureDir(dir); err != nil {
		return result, err
	}
	result = keygenResult{PrivateKeyPath: filepath.Join(dir, "identity"), PublicKeyPath: filepath.Join(dir, "identity.pub"), Fingerprint: ssh.FingerprintSHA256(publicKey)}
	for _, entry := range []struct {
		path string
		data []byte
	}{{result.PrivateKeyPath, pem.EncodeToMemory(block)}, {result.PublicKeyPath, ssh.MarshalAuthorizedKey(publicKey)}} {
		created, err := writeKeygenFile(entry.path, entry.data, secureFile)
		if created != nil {
			owned = append(owned, *created)
		}
		if err != nil {
			return keygenResult{}, err
		}
	}
	return result, nil
}
func writeKeygenFile(path string, data []byte, secure func(string) error) (owned *keygenOwnedFile, resultErr error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	owned = &keygenOwnedFile{path: path, info: info}
	if err := secure(path); err != nil {
		return owned, err
	}
	_, err = file.Write(data)
	return owned, err
}
func rollbackKeyPair(dir string, dirInfo os.FileInfo, owned []keygenOwnedFile) error {
	var failures []error
	for i := len(owned) - 1; i >= 0; i-- {
		file := owned[i]
		current, err := os.Lstat(file.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if os.SameFile(file.info, current) {
			if err := os.Remove(file.path); err != nil {
				failures = append(failures, err)
			}
		}
	}
	current, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return errors.Join(failures...)
	}
	if err != nil {
		failures = append(failures, err)
	} else if os.SameFile(dirInfo, current) {
		if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
			entries, readErr := os.ReadDir(dir)
			if readErr != nil || len(entries) == 0 {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}
