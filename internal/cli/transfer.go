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
	return transferWithProgress(parent, command, args, stdout, stderr, stderr)
}

func transferWithProgress(parent context.Context, command string, args []string, stdout, stderr, progressWriter io.Writer) (returnErr error) {
	f := flags(command, stderr)
	retry := clientRetryFlags(f)
	iface := f.String("interface", "", "direct network interface")
	timeout := f.Duration("timeout", 5*time.Minute, "maximum transfer duration")
	asJSON := f.Bool("json", false, "emit transfer summary as JSON")
	recursive := f.Bool("recursive", false, "transfer a directory tree; reject symlinks and existing destinations")
	maxFiles := f.Int("max-files", 10000, "maximum files in a recursive transfer (1..100000)")
	maxDepth := f.Int("max-depth", 32, "maximum recursive nesting (0..128)")
	resume := f.Bool("resume", false, "resume one file using an owned partial and private journal")
	resumeState := f.String("resume-state", "", "private local resume manifest; default uses the user cache")
	progressMode := f.String("progress", "", "jsonl emits progress events on stderr")
	var exclude transferExclusions
	f.Var(&exclude, "exclude", "exclude a relative slash glob or basename; repeatable, no **")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	positional := f.Args()
	if len(positional) != 3 || *timeout <= 0 {
		return fmt.Errorf("usage: shellpier %s [--interface NAME] [--timeout 5m] [--json] TICKET SOURCE DESTINATION", command)
	}
	for _, value := range positional[1:] {
		if value == "" || strings.ContainsRune(value, 0) {
			return errors.New("source and destination paths must be nonempty and contain no NUL")
		}
	}
	if *maxFiles < 1 || *maxFiles > 100000 || *maxDepth < 0 || *maxDepth > 128 || *recursive && (*resume || *resumeState != "") || *resumeState != "" && !*resume || *progressMode != "" && *progressMode != "jsonl" || len(exclude) > 0 && !*recursive {
		return errors.New("invalid recursive/resume/progress options or capacity limits")
	}
	if err := retry.Validate(); err != nil {
		return err
	}
	parent = retry.Context(parent)
	ticket, err := access.Read(positional[0])
	if err != nil {
		return err
	}
	if err = ticket.Validate("access"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	client, err := access.DialRetry(ctx, ticket, *iface, retry.RetryOptions)
	if err != nil {
		return err
	}
	defer client.Close()
	defer func() { returnErr = access.ConnectionError(client, returnErr) }()
	sf, err := sftp.NewClient(client)
	if err != nil {
		return err
	}
	defer sf.Close()
	src, dst := transferFS{}, transferFS{remote: sf}
	if command == "download" {
		src, dst = dst, src
	} else if command != "upload" {
		return errors.New("unsupported transfer direction")
	}
	progress := func(n int64) error {
		if *progressMode == "" {
			return nil
		}
		return json.NewEncoder(progressWriter).Encode(struct {
			Event string `json:"event"`
			Bytes int64  `json:"bytes"`
		}{"transfer_progress", n})
	}
	result := transferSummary{Command: command, Warnings: []string{}}
	switch {
	case *recursive:
		result, err = runTransferTree(ctx, src, dst, positional[1], positional[2], exclude, *maxFiles, *maxDepth, progress)
	case *resume:
		result, err = resumeTransfer(ctx, src, dst, positional[1], positional[2], *resumeState, command, ticket.HostKey, progress)
	default:
		copyCtx := context.WithValue(ctx, transferProgressKey{}, progress)
		if command == "download" {
			result.Bytes, err = downloadFile(copyCtx, sf, positional[1], positional[2])
		} else {
			result.Bytes, err = uploadFile(copyCtx, sf, positional[1], positional[2])
		}
		if err == nil {
			result.SHA256, _, err = transferDigest(ctx, dst, positional[2])
			result.Files = 1
		}
	}
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	result.Command = command
	if result.Warnings == nil {
		result.Warnings = []string{}
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(result)
	}
	_, err = fmt.Fprintf(stdout, "Transferred %d bytes in %d files and %d directories.\n", result.Bytes, result.Files, result.Directories)
	for _, warning := range result.Warnings {
		if _, e := fmt.Fprintln(stdout, "Warning:", warning); e != nil {
			return e
		}
	}
	return err
}

type transferProgressKey struct{}

func transferProgress(ctx context.Context) func(int64) error {
	callback, _ := ctx.Value(transferProgressKey{}).(func(int64) error)
	return callback
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
	count, err = transferCopy(ctx, output, input, transferProgress(ctx))
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
	count, copyErr := transferCopy(ctx, output, input, transferProgress(ctx))
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
