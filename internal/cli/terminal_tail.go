package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
)

// terminalTail reads the existing worker; cancellation never closes its session.
func terminalTail(parent context.Context, args []string, stdout, stderr io.Writer) error {
	f := flags("terminal tail", stderr)
	state := f.String("state", "", "private terminal state file")
	cursor := f.Uint64("cursor", 0, "absolute output byte offset")
	follow := f.Bool("follow", false, "continue reading until terminal output closes")
	timeout := f.Duration("timeout", 30*time.Minute, "total reader lifetime, positive and at most 24h")
	machine := f.Bool("json", false, "unsupported for raw terminal output")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	if *machine {
		return &UsageError{Err: errors.New("terminal tail carries raw bytes and does not support --json")}
	}
	if f.NArg() != 0 || *state == "" || *timeout <= 0 || *timeout > 24*time.Hour {
		return &UsageError{Err: errors.New("tail requires --state FILE and --timeout greater than zero and at most 24h")}
	}
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	// io.Writer alone cannot interrupt a permanently blocked Write. A closable
	// output belongs to this reader and is closed only when its context ends.
	stop := context.AfterFunc(ctx, func() {
		if output, ok := stdout.(io.Closer); ok {
			_ = output.Close()
		}
	})
	defer stop()
	limit, wait := terminalCapacity, time.Duration(0)
	if *follow {
		limit, wait = terminalDefaultRead, 30*time.Second
	}
	var identity terminalState
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var encoded bytes.Buffer
		readArgs := []string{"--state", *state, "--cursor", strconv.FormatUint(*cursor, 10), "--limit", strconv.Itoa(limit), "--wait", wait.String()}
		err := terminalControlWithIdentity(ctx, "read", readArgs, nil, &encoded, stderr, &identity)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		var result terminalReadResult
		if err := json.Unmarshal(encoded.Bytes(), &result); err != nil {
			return errors.New("invalid terminal tail read response")
		}
		if result.NextCursor < *cursor || result.Dropped > result.NextCursor-*cursor || uint64(len(result.Data)) != result.NextCursor-*cursor-result.Dropped {
			return errors.New("invalid terminal tail output cursor")
		}
		if result.Dropped > 0 {
			if _, err := fmt.Fprintf(stderr, "terminal tail: dropped %d output bytes before cursor %d\n", result.Dropped, *cursor+result.Dropped); err != nil {
				return err
			}
		}
		if len(result.Data) > 0 {
			n, err := stdout.Write(result.Data)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				return err
			}
			if n != len(result.Data) {
				return io.ErrShortWrite
			}
		}
		*cursor = result.NextCursor
		if !*follow || result.Closed && len(result.Data) < limit {
			return nil
		}
	}
}
