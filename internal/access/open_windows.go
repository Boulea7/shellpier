package access

import (
	"os"

	"github.com/Boulea7/shellpier/internal/platform"
)

// Share deletion so atomic state replacement can coexist with bounded readers.
func openTicket(path string) (*os.File, error) { return platform.OpenRegular(path) }

// Capture file IDs from the handle now: path-based Windows FileInfo loads IDs
// lazily using an exclusive handle, which races readers and replacements.
func statTicket(path string) (os.FileInfo, error) {
	file, err := platform.OpenRegular(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.Stat()
}
