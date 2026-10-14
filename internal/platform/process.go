package platform

import (
	"fmt"
	"os/exec"
	"os/user"
	"runtime"
)

func Identity() (string, error) {
	u, e := user.Current()
	if e != nil {
		return "", e
	}
	return u.Username, nil
}

// Command executes an explicitly authorized command using a trusted shell.
func Command(shell, command, dir string) (*exec.Cmd, error) {
	var cmd *exec.Cmd
	if shell == "" || shell == "auto" {
		if runtime.GOOS == "windows" {
			if _, e := exec.LookPath("powershell.exe"); e == nil {
				shell = "powershell"
			} else {
				shell = "cmd"
			}
		} else {
			shell = "sh"
		}
	}
	switch shell {
	case "sh":
		cmd = exec.Command("sh", "-c", command)
	case "cmd":
		if runtime.GOOS != "windows" {
			return nil, fmt.Errorf("cmd requires Windows")
		}
		cmd = exec.Command("cmd.exe", "/d", "/s", "/c", command)
	case "powershell":
		if runtime.GOOS != "windows" {
			return nil, fmt.Errorf("powershell requires Windows")
		}
		cmd = exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "$OutputEncoding = [Console]::InputEncoding = [Console]::OutputEncoding = New-Object System.Text.UTF8Encoding; "+command)
	default:
		return nil, fmt.Errorf("unsupported shell %q", shell)
	}
	cmd.Dir = dir
	return cmd, nil
}
