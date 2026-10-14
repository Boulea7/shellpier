//go:build !windows

package platform

import "os"

func SecureFile(path string) error { return os.Chmod(path, 0600) }
func SecureDir(path string) error  { return os.Chmod(path, 0700) }
