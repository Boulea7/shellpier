package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"reflect"
	"testing"
)

func TestGlobalJSONHelpAndValueBoundaries(t *testing.T) {
	for _, args := range [][]string{{"--json"}, {"--json", "exec", "--help"}, {"exec", "--help", "--json"}, {"--json", "terminal", "read", "--help"}, {"--json", "help", "files"}} {
		var stdout, stderr bytes.Buffer
		if err := Run(context.Background(), args, nil, &stdout, &stderr, "test"); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !json.Valid(stdout.Bytes()) || stderr.Len() != 0 {
			t.Fatalf("%v did not return one JSON help document: stdout=%q stderr=%q", args, stdout.String(), stderr.String())
		}
	}
	if localHelpRequested([]string{"exec", "--command", "--help", "@missing"}) || localHelpRequested([]string{"exec", "@missing", "--", "--help"}) {
		t.Fatal("remote command text was interpreted as local help")
	}
	if err := Run(context.Background(), []string{"help", "files", "read"}, nil, io.Discard, io.Discard, "test"); err != nil {
		t.Fatal(err)
	}
}

func TestLocalOptionsRequireValuesAndPreserveDelimiterValues(t *testing.T) {
	for _, args := range [][]string{{"--out"}, {"@ticket", "--out"}} {
		f := flag.NewFlagSet("test", flag.ContinueOnError)
		out := f.String("out", "", "")
		var usage *UsageError
		if err := parseFlags(f, args); !errors.As(err, &usage) || *out != "" {
			t.Fatalf("missing value was accepted: %v, value=%q, error=%v", args, *out, err)
		}
	}
	f := flag.NewFlagSet("test", flag.ContinueOnError)
	command := f.String("command", "", "")
	f.Bool("json", false, "")
	args := []string{"@ticket", "--command", "--", "--json"}
	local, remote, separated := splitCommandArgs(f, args)
	if separated || len(remote) != 0 || parseFlags(f, local) != nil || *command != "--" || !reflect.DeepEqual(f.Args(), []string{"@ticket"}) {
		t.Fatal("a flag value was treated as the remote command separator")
	}
	local, remote, separated = splitCommandArgs(f, []string{"--command=literal", "@ticket", "--", "--json", "--help"})
	if !separated || !reflect.DeepEqual(local, []string{"--command=literal", "@ticket"}) || !reflect.DeepEqual(remote, []string{"--json", "--help"}) {
		t.Fatal("remote command arguments were parsed as local options")
	}
}
