package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

var completionCommands = commandNames()

func completionFlagWords(spec commandSpec) string {
	words := []string{"--help"}
	for _, f := range spec.Flags {
		words = append(words, "--"+f.Name)
	}
	return strings.Join(words, " ")
}
func completionSpecs() []commandSpec {
	specs := []commandSpec{}
	for _, spec := range commandSpecs {
		specs = append(specs, spec)
		for _, sub := range spec.Subcommands {
			sub.Name = spec.Name + " " + sub.Name
			specs = append(specs, sub)
		}
	}
	return specs
}
func completionCases() string {
	var lines []string
	for _, spec := range completionSpecs() {
		lines = append(lines, "        '"+spec.Name+"') options='"+completionFlagWords(spec)+"' ;;")
	}
	return strings.Join(lines, "\n")
}

// completion emits static shell integrations without changing shell profiles.
func completion(args []string, stdout, stderr io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(stdout, "Usage: shellpier completion bash|zsh|fish|powershell\nPrint a completion script; review it and load it explicitly. PowerShell requires 7+.")
		return err
	}
	if len(args) != 1 {
		return errors.New("usage: shellpier completion bash|zsh|fish|powershell")
	}
	for _, spec := range completionSpecs() {
		for _, word := range strings.Fields(spec.Name) {
			if !safeCompletionToken(word) {
				return errors.New("unsafe completion command token")
			}
		}
		for _, flag := range spec.Flags {
			if !safeCompletionToken(flag.Name) {
				return errors.New("unsafe completion flag token")
			}
		}
	}
	completionCommands := commandNames()
	var script string
	switch args[0] {
	case "bash":
		script = `# Load explicitly in Bash after reviewing this script.
_shellpier_complete() {
    local current="${COMP_WORDS[COMP_CWORD]}"
    local -a tokens=("${COMP_WORDS[@]:1:$((COMP_CWORD-1))}")
    COMPREPLY=()
` + completionShellContext() + `
    if [[ -z "$command_name" ]]; then
        COMPREPLY=( $(compgen -W '` + completionCommands + ` --json' -- "$current") )
    elif [[ "$command_name" == completion ]]; then
        COMPREPLY=( $(compgen -W 'bash zsh fish powershell' -- "$current") )
    elif [[ -z "$action" && "$current" != --* ]]; then
        local actions=''
        case "$command_name" in
` + completionActionCases() + `
        esac
        COMPREPLY=( $(compgen -W "$actions" -- "$current") )
    elif [[ "$current" == --* ]]; then
        local options='--help'
        case "$key" in
` + completionCases() + `
        esac
        COMPREPLY=( $(compgen -W "$options" -- "$current") )
    fi
}
complete -F _shellpier_complete shellpier shellpier.exe
`
	case "zsh":
		script = `# Load explicitly after compinit in Zsh.
_shellpier_complete() {
    local current="${words[CURRENT]}"
    local -a tokens=("${words[@]:1:$((CURRENT-2))}")
` + completionShellContext() + `
    if [[ -z "$command_name" ]]; then
        local -a commands
        commands=(` + completionCommands + ` --json)
        compadd -- "${commands[@]}"
    elif [[ "$command_name" == completion ]]; then
        compadd -- bash zsh fish powershell
    elif [[ -z "$action" && "$current" != --* ]]; then
        local actions=''
        case "$command_name" in
` + completionActionCases() + `
        esac
        compadd -- ${(s: :)actions}
    elif [[ "$current" == --* ]]; then
        local options='--help'
        case "$key" in
` + completionCases() + `
        esac
        compadd -- ${(s: :)options}
    fi
}
compdef _shellpier_complete shellpier shellpier.exe
`
	case "fish":
		lines := []string{"# Load explicitly in Fish; no executable is invoked.", completionFishContext()}
		for _, executable := range []string{"shellpier", "shellpier.exe"} {
			lines = append(lines, "complete -c "+executable+" -f")
			lines = append(lines, "complete -c "+executable+" -n '__shellpier_context command' -a '"+completionCommands+"'")
			lines = append(lines, "complete -c "+executable+" -n '__shellpier_context active' -l help -d 'Show help'")
			lines = append(lines, "complete -c "+executable+" -n '__shellpier_context key completion' -a 'bash zsh fish powershell'")
			lines = append(lines, "complete -c "+executable+" -n '__shellpier_context command' -l json")
			for _, parent := range commandSpecs {
				if len(parent.Subcommands) > 0 {
					lines = append(lines, "complete -c "+executable+" -n '__shellpier_context action "+parent.Name+"' -a '"+completionActions(parent)+"'")
				}
			}
			for _, spec := range completionSpecs() {
				for _, flag := range spec.Flags {
					condition := "__shellpier_context key \"" + spec.Name + "\""
					line := "complete -c " + executable + " -n '" + condition + "' -l " + flag.Name
					if flag.Type != "bool" {
						line += " -r"
					}
					lines = append(lines, line)
				}
			}
		}
		script = strings.Join(lines, "\n") + "\n"
	case "powershell":
		script = `# Requires PowerShell 7+. Review and load explicitly.
if ($PSVersionTable.PSVersion.Major -lt 7) { throw 'ShellPier completion requires PowerShell 7+.' }
$ShellPierFlags = @{
` + completionPowerShellFlags() + `
}
$ShellPierActions = @{
` + completionPowerShellActions() + `
}
$ShellPierValueFlags = @{
` + completionPowerShellValueFlags() + `
}
$ShellPierCompleter = {
    param($wordToComplete, $commandAst, $cursorPosition)
    $elements = @($commandAst.CommandElements)
    $subcommands = '` + completionCommands + `'.Split(' ')
    $candidates = @()
    $commandName = ''
    $key = ''
    $action = ''
    $valuePending = $false
    for ($index = 1; $index -lt $elements.Count; $index++) {
        $node = $elements[$index]
        if ($node.Extent.StartOffset -ge $cursorPosition -or $node.Extent.EndOffset -ge $cursorPosition) { break }
        $arg = $node.Extent.Text
        if ($node -is [System.Management.Automation.Language.StringConstantExpressionAst]) { $arg = $node.Value }
        if ($valuePending) { $valuePending = $false; continue }
        if ($arg -eq '--') { return }
        if (-not $commandName) {
            if ($arg -eq '--json' -or $arg.StartsWith('--json=')) { continue }
            if (-not $arg.StartsWith('-')) { $commandName = $arg; $key = $arg }
            continue
        }
        if (-not $action -and $ShellPierActions.ContainsKey($commandName) -and -not $arg.StartsWith('-')) {
            $action = $arg
            $key = $commandName + ' ' + $action
            continue
        }
        if ($arg.StartsWith('-') -and -not $arg.Contains('=') -and $ShellPierValueFlags.ContainsKey($key)) {
            if ($ShellPierValueFlags[$key].Split(' ') -contains $arg) { $valuePending = $true }
        }
    }
    if ($valuePending) { return }
    if (-not $commandName) {
        $candidates = $subcommands + '--json'
    } elseif ($commandName -eq 'completion') {
        $candidates = @('bash', 'zsh', 'fish', 'powershell')
    } elseif (-not $action -and $ShellPierActions.ContainsKey($commandName) -and -not $wordToComplete.StartsWith('--')) {
        $candidates = $ShellPierActions[$commandName].Split(' ')
    } elseif ($wordToComplete.StartsWith('--')) {
        if ($ShellPierFlags.ContainsKey($key)) { $candidates = $ShellPierFlags[$key].Split(' ') }
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

func completionPowerShellFlags() string {
	var lines []string
	for _, spec := range completionSpecs() {
		lines = append(lines, "    '"+spec.Name+"' = '"+completionFlagWords(spec)+"'")
	}
	return strings.Join(lines, "\n")
}

func safeCompletionToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r != '-' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func completionActions(spec commandSpec) string {
	names := []string{}
	for _, sub := range spec.Subcommands {
		names = append(names, sub.Name)
	}
	return strings.Join(names, " ")
}
func completionActionCases() string {
	lines := []string{}
	for _, spec := range commandSpecs {
		if len(spec.Subcommands) > 0 {
			lines = append(lines, "        '"+spec.Name+"') actions='"+completionActions(spec)+"' ;;")
		}
	}
	return strings.Join(lines, "\n")
}
func completionPowerShellActions() string {
	lines := []string{}
	for _, spec := range commandSpecs {
		if len(spec.Subcommands) > 0 {
			lines = append(lines, "    '"+spec.Name+"' = '"+completionActions(spec)+"'")
		}
	}
	return strings.Join(lines, "\n")
}

// Context parsing consumes only known local option values, without evaluating them.
func completionShellContext() string {
	return `    local command_name='' action='' key='' value_pending=0 json_prefix=0 arg
    for arg in "${tokens[@]}"; do
        if [[ "$value_pending" == 1 ]]; then value_pending=0; continue; fi
        if [[ "$arg" == -- ]]; then return; fi
        if [[ -z "$command_name" ]]; then
            # Bash may split '=' into its own completion word.
            if [[ "$json_prefix" == 1 && "$arg" == = ]]; then json_prefix=2; continue; fi
            if [[ "$json_prefix" == 2 && ( "$arg" == true || "$arg" == false ) ]]; then json_prefix=0; continue; fi
            json_prefix=0
            if [[ "$arg" == --json ]]; then json_prefix=1; continue; fi
            if [[ "$arg" == --json=* ]]; then continue; fi
            if [[ "$arg" != -* ]]; then command_name="$arg"; key="$arg"; fi
            continue
        fi
        if [[ -z "$action" && "$arg" != -* ]] && [[ "$command_name" == files || "$command_name" == terminal ]]; then
            action="$arg"; key="$command_name $action"; continue
        fi
        if [[ "$arg" == -* && "$arg" != *=* ]]; then
            case "$key|$arg" in
` + completionValueCases() + `
            esac
        fi
    done
    if [[ "$value_pending" == 1 ]]; then return; fi
`
}
func completionValueWords(spec commandSpec) string {
	words := []string{}
	for _, flag := range spec.Flags {
		if flag.Type != "bool" {
			words = append(words, "--"+flag.Name, "-"+flag.Name)
		}
	}
	return strings.Join(words, " ")
}
func completionValueCases() string {
	lines := []string{}
	for _, spec := range completionSpecs() {
		for _, word := range strings.Fields(completionValueWords(spec)) {
			lines = append(lines, "                '"+spec.Name+"|"+word+"') value_pending=1 ;;")
		}
	}
	return strings.Join(lines, "\n")
}
func completionPowerShellValueFlags() string {
	lines := []string{}
	for _, spec := range completionSpecs() {
		lines = append(lines, "    '"+spec.Name+"' = '"+completionValueWords(spec)+"'")
	}
	return strings.Join(lines, "\n")
}

func completionFishContext() string {
	cases := []string{}
	for _, spec := range completionSpecs() {
		for _, word := range strings.Fields(completionValueWords(spec)) {
			cases = append(cases, "                case '"+spec.Name+"|"+word+"'\n                    set value_pending 1")
		}
	}
	return `function __shellpier_context
    set -l tokens (commandline -opc)
    set -l command_name ''
    set -l action ''
    set -l key ''
    set -l value_pending 0
    for arg in $tokens[2..-1]
        if test "$value_pending" = 1
            set value_pending 0
            continue
        end
        if test "$arg" = --
            return 1
        end
        if test -z "$command_name"
            if test "$arg" = --json; or string match -q -- '--json=*' "$arg"
                continue
            end
            if not string match -q -- '-*' "$arg"
                set command_name "$arg"
                set key "$arg"
            end
            continue
        end
        if test -z "$action"; and not string match -q -- '-*' "$arg"; and contains -- "$command_name" files terminal
            set action "$arg"
            set key "$command_name $action"
            continue
        end
        if string match -q -- '-*' "$arg"; and not string match -q -- '*=*' "$arg"
            switch "$key|$arg"
` + strings.Join(cases, "\n") + `
            end
        end
    end
    if test "$value_pending" = 1
        return 1
    end
    switch $argv[1]
        case command
            test -z "$command_name"
        case key
            test "$key" = "$argv[2]"
        case action
            test "$command_name" = "$argv[2]"; and test -z "$action"
        case active
            return 0
        case '*'
            return 1
    end
end`
}
