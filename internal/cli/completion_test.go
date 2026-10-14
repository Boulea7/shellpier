package cli

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
)

func TestCompletionOutputs(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			if err := completion([]string{shell}, &out, &diagnostics); err != nil {
				t.Fatal(err)
			}
			if out.Len() == 0 || !strings.Contains(out.String(), "keygen") {
				t.Fatal("completion output is missing the new command")
			}
			if diagnostics.Len() != 0 {
				t.Fatal("successful script generation emitted diagnostics")
			}
		})
	}
}

func TestCompletionRejectsInvalidInput(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"bash", "extra"}} {
		var out bytes.Buffer
		if err := completion(args, &out, io.Discard); err == nil {
			t.Fatalf("accepted invalid arguments %q", args)
		}
		if out.Len() != 0 {
			t.Fatalf("invalid arguments %q emitted a partial script", args)
		}
	}
}

func TestCompletionHelp(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		var out bytes.Buffer
		if err := completion([]string{flag}, &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "Usage:") {
			t.Fatal("help did not describe usage")
		}
	}
}

type completionFailWriter struct{ err error }

func (w completionFailWriter) Write([]byte) (int, error) { return 0, w.err }

func TestCompletionPropagatesWriteFailure(t *testing.T) {
	failure := errors.New("output closed")
	for _, args := range [][]string{{"bash"}, {"zsh"}, {"fish"}, {"powershell"}, {"--help"}} {
		if err := completion(args, completionFailWriter{failure}, io.Discard); !errors.Is(err, failure) {
			t.Fatalf("arguments %q: expected writer error, got %v", args, err)
		}
	}
}

func TestCompletionBashLocalContext(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash is unavailable")
	}
	var script bytes.Buffer
	if err := completion([]string{"bash"}, &script, io.Discard); err != nil {
		t.Fatal(err)
	}
	script.WriteString(`
assert_has() { _shellpier_complete; [[ " ${COMPREPLY[*]} " == *" $1 "* ]] || exit 1; }
assert_none() { _shellpier_complete; [[ ${#COMPREPLY[@]} == 0 ]] || exit 1; }
COMP_WORDS=(shellpier --json te); COMP_CWORD=2; assert_has terminal
COMP_WORDS=(shellpier --json=true terminal write --te); COMP_CWORD=4; assert_has --text
COMP_WORDS=(shellpier --json=false terminal write --te); COMP_CWORD=4; assert_has --text
COMP_WORDS=(shellpier --json = true terminal write --te); COMP_CWORD=6; assert_has --text
COMP_WORDS=(shellpier --json exec @ticket -- echo --max); COMP_CWORD=6; assert_none
COMP_WORDS=(shellpier --json exec --command -- @ticket --max); COMP_CWORD=6; assert_has --max-output
COMP_WORDS=(shellpier terminal write --text -- --state file --te); COMP_CWORD=7; assert_has --text
COMP_WORDS=(shellpier exec --command --max); COMP_CWORD=3; assert_none
`)
	cmd := exec.Command(bash, "-s")
	cmd.Dir = t.TempDir()
	cmd.Stdin = &script
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Bash completion context: %v\n%s", err, out)
	}
}
