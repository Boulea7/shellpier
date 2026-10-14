//go:build linux

package platform

import (
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func terminalSessionGroups(session int) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	groups := map[int]bool{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid < 1 {
			continue
		}
		sid, err := unix.Getsid(pid)
		if err != nil || sid != session {
			continue
		}
		group, err := unix.Getpgid(pid)
		if err == nil && group > 0 {
			groups[group] = true
		}
	}
	result := make([]int, 0, len(groups))
	for group := range groups {
		result = append(result, group)
	}
	return result, nil
}
