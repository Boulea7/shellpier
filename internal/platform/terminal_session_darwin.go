//go:build darwin

package platform

import "golang.org/x/sys/unix"

func terminalSessionGroups(session int) ([]int, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	groups := map[int]bool{}
	for _, process := range processes {
		pid := int(process.Proc.P_pid)
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
