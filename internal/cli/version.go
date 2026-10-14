package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
)

type buildVersion struct {
	Version     string `json:"version"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Go          string `json:"go"`
	VCSRevision string `json:"vcs_revision,omitempty"`
	VCSTime     string `json:"vcs_time,omitempty"`
	VCSModified bool   `json:"vcs_modified"`
}

func printVersion(args []string, stdout, stderr io.Writer, version string) error {
	f := flags("version", stderr)
	asJSON := f.Bool("json", false, "print version and build metadata")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return errors.New("version accepts flags only")
	}
	if !*asJSON {
		_, err := fmt.Fprintln(stdout, "shellpier", version)
		return err
	}
	info := buildVersion{Version: version, OS: runtime.GOOS, Arch: runtime.GOARCH, Go: runtime.Version()}
	if build, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				info.VCSRevision = setting.Value
			case "vcs.time":
				info.VCSTime = setting.Value
			case "vcs.modified":
				info.VCSModified = setting.Value == "true"
			}
		}
	}
	return json.NewEncoder(stdout).Encode(info)
}
