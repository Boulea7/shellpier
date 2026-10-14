package cli

import (
	"bytes"
	"errors"
	"io"
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
