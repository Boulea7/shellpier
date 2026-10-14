package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Boulea7/shellpier/internal/access"
	"github.com/Boulea7/shellpier/internal/platform"
	"github.com/pkg/sftp"
)

func transfer(parent context.Context, command string, args []string, stdout, stderr io.Writer) error {
	f := flags(command, stderr)
	iface := f.String("interface", "", "direct network interface")
	timeout := f.Duration("timeout", 5*time.Minute, "maximum transfer duration")
	asJSON := f.Bool("json", false, "emit transferred byte count as JSON")
	// Permit the documented positional spelling followed by options.
	var positional []string
	for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		positional = append(positional, args[0])
		args = args[1:]
	}
	if err := f.Parse(args); err != nil {
		return err
	}
	positional = append(positional, f.Args()...)
	if len(positional) != 3 || *timeout <= 0 {
		return fmt.Errorf("usage: shellpier %s [--interface NAME] [--timeout 5m] [--json] TICKET SOURCE DESTINATION", command)
	}
	for _, value := range positional[1:] {
		if value == "" || strings.ContainsRune(value, 0) {
			return errors.New("source and destination paths must be nonempty and contain no NUL")
		}
	}
	ticket, err := access.Read(positional[0])
	if err != nil {
		return err
	}
	if err = ticket.Validate("access"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	client, err := access.Dial(ctx, ticket, *iface)
	if err != nil {
		return err
	}
	defer client.Close()
	sf, err := sftp.NewClient(client)
	if err != nil {
		return err
	}
	defer sf.Close()
	var count int64
	switch command {
	case "download":
		count, err = downloadFile(ctx, sf, positional[1], positional[2])
	case "upload":
		count, err = uploadFile(ctx, sf, positional[1], positional[2])
	default:
		return errors.New("unsupported transfer direction")
	}
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(struct {
			Command string `json:"command"`
			Bytes   int64  `json:"bytes"`
		}{command, count})
	}
	_, err = fmt.Fprintf(stdout, "Transferred %d bytes.\n", count)
	return err
}

func openTransferSource(path string) (*os.File, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("local source must be a regular file")
	}
	file, err := platform.OpenRegular(path)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		file.Close()
		return nil, errors.New("local source changed while opening")
	}
	return file, nil
}

func downloadFile(ctx context.Context, sf *sftp.Client, source, destination string) (count int64, result error) {
	before, err := sf.Lstat(source)
	if err != nil {
		return 0, err
	}
	if !before.Mode().IsRegular() {
		return 0, errors.New("remote source must be a regular file")
	}
	input, err := sf.Open(source)
	if err != nil {
		return 0, err
	}
	inputClosed := false
	defer func() {
		if !inputClosed {
			result = errors.Join(result, input.Close())
		}
	}()
	actual, err := input.Stat()
	if err != nil {
		return 0, err
	}
	if !actual.Mode().IsRegular() {
		return 0, errors.New("opened remote source is not a regular file")
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return 0, err
	}
	created, err := output.Stat()
	if err != nil {
		output.Close()
		return 0, err
	}
	success := false
	defer func() {
		if !success {
			if current, e := os.Lstat(destination); e == nil && os.SameFile(created, current) {
				result = errors.Join(result, os.Remove(destination))
			}
		}
	}()
	if err = platform.SecureFile(destination); err != nil {
		output.Close()
		return 0, err
	}
	// Fixed-size copying prevents the source size from becoming an allocation size.
	count, err = io.CopyBuffer(writerOnly{output}, readerOnly{input}, make([]byte, 64*1024))
	closeErr := output.Close()
	sourceCloseErr := input.Close()
	inputClosed = true
	if err != nil || closeErr != nil || sourceCloseErr != nil {
		return count, errors.Join(err, closeErr, sourceCloseErr)
	}
	if err = ctx.Err(); err != nil {
		return count, err
	}
	success = true
	return count, nil
}

type writerOnly struct{ io.Writer }
type readerOnly struct{ io.Reader }

func uploadFile(ctx context.Context, sf *sftp.Client, source, destination string) (count int64, result error) {
	input, err := openTransferSource(source)
	if err != nil {
		return 0, err
	}
	inputClosed := false
	defer func() {
		if !inputClosed {
			result = errors.Join(result, input.Close())
		}
	}()
	output, err := sf.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		// The peer can create the file before a disconnected response reaches us.
		if ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, incompleteUpload(destination, errors.Join(err, ctx.Err()))
		}
		return 0, err
	}
	count, copyErr := io.CopyBuffer(writerOnly{output}, readerOnly{input}, make([]byte, 64*1024))
	closeErr := output.Close()
	sourceCloseErr := input.Close()
	inputClosed = true
	if err = errors.Join(copyErr, closeErr, sourceCloseErr, ctx.Err()); err != nil {
		return count, incompleteUpload(destination, err)
	}
	return count, nil
}

func incompleteUpload(destination string, err error) error {
	return fmt.Errorf("transfer interrupted: %w; incomplete destination may remain: %s; inspect before retrying", err, destination)
}
