package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/pkg/sftp"
)

// filesCopy streams an independent copy through the client, without shell access.
func filesCopy(ctx context.Context, sf *sftp.Client, source, destination string, dryRun, asJSON bool, out io.Writer) error {
	info, err := sf.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("copy source must be a regular file, not a symlink")
	}
	if _, err = sf.Lstat(destination); err == nil {
		return errors.New("destination already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = filesParentDirectory(sf, destination, false); err != nil {
		return err
	}
	var count int64
	if !dryRun {
		input, err := sf.Open(source)
		if err != nil {
			return err
		}
		actual, statErr := input.Stat()
		if statErr != nil || !actual.Mode().IsRegular() {
			return errors.Join(statErr, errors.New("opened copy source must be a regular file"), input.Close())
		}
		output, err := sf.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
		if err != nil {
			closeErr := input.Close()
			if ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return incompleteUpload(destination, errors.Join(err, closeErr, ctx.Err()))
			}
			return errors.Join(err, closeErr)
		}
		var copyErr error
		count, copyErr = transferCopy(ctx, output, input, nil)
		closeErr := output.Close()
		sourceCloseErr := input.Close()
		if err = errors.Join(copyErr, closeErr, sourceCloseErr, ctx.Err()); err != nil {
			// SFTP v3 exposes no stable identity to safely unlink a possibly replaced path.
			return incompleteUpload(destination, err)
		}
	}
	result := struct {
		Action      string `json:"action"`
		Path        string `json:"path"`
		Destination string `json:"destination"`
		Bytes       int64  `json:"bytes"`
		DryRun      bool   `json:"dry_run"`
		Transport   string `json:"transport"`
	}{"copy", source, destination, count, dryRun, "client-stream"}
	if asJSON {
		return json.NewEncoder(out).Encode(result)
	}
	verb := "Copied"
	if dryRun {
		verb = "Would copy"
	}
	_, err = fmt.Fprintf(out, "%s %s -> %s (%d bytes; client-stream)\n", verb, source, destination, count)
	return err
}
