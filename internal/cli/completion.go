package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

const completionCommands = "share host invite connect proxy ssh-config doctor status stop revoke cleanup relay inspect check upload download keygen completion version help"

// completion emits static shell integrations without changing shell profiles.
func completion(args []string, stdout, stderr io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(stdout, "Usage: shellpier completion bash|zsh|fish|powershell\nPrint a completion script; review it and load it explicitly. PowerShell requires 7+.")
		return err
	}
	if len(args) != 1 {
		return errors.New("usage: shellpier completion bash|zsh|fish|powershell")
	}
	var script string
	switch args[0] {
	case "bash":
		script = `# Load explicitly in Bash after reviewing this script.
_shellpier_complete() {
    local current="${COMP_WORDS[COMP_CWORD]}"
    COMPREPLY=()
    if [ "$COMP_CWORD" -eq 1 ]; then
        COMPREPLY=( $(compgen -W '` + completionCommands + `' -- "$current") )
    elif [ "${COMP_WORDS[1]}" = completion ]; then
        COMPREPLY=( $(compgen -W 'bash zsh fish powershell' -- "$current") )
    elif [[ "$current" == --* ]]; then
        COMPREPLY=( $(compgen -W '--help' -- "$current") )
    fi
}
complete -F _shellpier_complete shellpier shellpier.exe
`
	case "zsh":
		script = `# Load explicitly after compinit in Zsh.
_shellpier_complete() {
    local -a commands shells
    commands=(` + completionCommands + `)
    shells=(bash zsh fish powershell)
    if (( CURRENT == 2 )); then
        compadd -- "${commands[@]}"
    elif [[ "${words[2]}" == completion ]]; then
        compadd -- "${shells[@]}"
    elif [[ "${words[CURRENT]}" == --* ]]; then
        compadd -- --help
    fi
}
compdef _shellpier_complete shellpier shellpier.exe
`
	case "fish":
		var lines []string
		lines = append(lines, "# Load explicitly in Fish; no executable is invoked.")
		for _, executable := range []string{"shellpier", "shellpier.exe"} {
			lines = append(lines, "complete -c "+executable+" -f")
			lines = append(lines, "complete -c "+executable+" -n '__fish_use_subcommand' -a '"+completionCommands+"'")
			lines = append(lines, "complete -c "+executable+" -n '__fish_seen_subcommand_from completion' -a 'bash zsh fish powershell'")
			lines = append(lines, "complete -c "+executable+" -l help -d 'Show help'")
		}
		script = strings.Join(lines, "\n") + "\n"
	case "powershell":
		script = `# Requires PowerShell 7+. Review and load explicitly.
if ($PSVersionTable.PSVersion.Major -lt 7) { throw 'ShellPier completion requires PowerShell 7+.' }
$ShellPierCompleter = {
    param($wordToComplete, $commandAst, $cursorPosition)
    $elements = @($commandAst.CommandElements)
    $subcommands = '` + completionCommands + `'.Split(' ')
    $candidates = @()
    if ($elements.Count -le 2 -and ($elements.Count -lt 2 -or $elements[1].Extent.StartOffset -le $cursorPosition -and $elements[1].Extent.EndOffset -ge $cursorPosition)) {
        $candidates = $subcommands
    } elseif ($elements.Count -ge 2 -and $elements[1].Extent.Text -eq 'completion') {
        $candidates = @('bash', 'zsh', 'fish', 'powershell')
    } elseif ($wordToComplete.StartsWith('--')) {
        $candidates = @('--help')
    }
    foreach ($candidate in $candidates) {
        if ($candidate.StartsWith($wordToComplete, [StringComparison]::OrdinalIgnoreCase)) {
            [System.Management.Automation.CompletionResult]::new($candidate, $candidate, 'ParameterValue', $candidate)
        }
    }
}
Register-ArgumentCompleter -Native -CommandName shellpier,shellpier.exe -ScriptBlock $ShellPierCompleter
# Register the resolved native path too, including installations containing spaces.
$ShellPierCommand = Get-Command shellpier.exe,shellpier -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
if ($ShellPierCommand) {
    Register-ArgumentCompleter -Native -CommandName $ShellPierCommand.Path -ScriptBlock $ShellPierCompleter
}
`
	default:
		return fmt.Errorf("unsupported completion shell %q; choose bash, zsh, fish, or powershell", args[0])
	}
	_, err := io.WriteString(stdout, script)
	return err
}
