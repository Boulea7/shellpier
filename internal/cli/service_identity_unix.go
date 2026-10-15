//go:build !windows

package cli

import (
	"os"
	"strconv"
)

func serviceUserID() (string, error) { return strconv.Itoa(os.Getuid()), nil }
