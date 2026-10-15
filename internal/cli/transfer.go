package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
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
	return transferWithInput(parent, command, args, nil, stdout, stderr)
}

func transferWithInput(parent context.Context, command string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	return transferWithInputAndProgress(parent, command, args, stdin, stdout, stderr, stderr)
}

func transferWithProgress(parent context.Context, command string, args []string, stdout, stderr, progressWriter io.Writer) error {
	return transferWithInputAndProgress(parent, command, args, nil, stdout, stderr, progressWriter)
}

func transferWithInputAndProgress(parent context.Context, command string, args []string, stdin io.Reader, stdout, stderr, progressWriter io.Writer) (returnErr error) {
	f := flags(command, stderr)
	retry := clientRetryFlags(f)
	iface := f.String("interface", "", "direct network interface")
	timeout := f.Duration("timeout", 5*time.Minute, "maximum transfer duration")
	asJSON := f.Bool("json", false, "emit transfer summary as JSON")
	recursive := f.Bool("recursive", false, "transfer a directory tree; reject symlinks and existing destinations")
	maxFiles := f.Int("max-files", 10000, "maximum recursive or selected files (1..100000)")
	maxDepth := f.Int("max-depth", 32, "maximum recursive or selected path nesting (0..128)")
	resume := f.Bool("resume", false, "resume one file using an owned partial and private journal")
	resumeState := f.String("resume-state", "", "private local resume manifest; default uses the user cache")
	progressMode := f.String("progress", "", "jsonl emits progress events on stderr")
	dryRun := f.Bool("dry-run", false, "read-only transfer plan; no destination or resume state is created")
	preserveTimes := f.Bool("preserve-times", false, "preserve new file/directory mtime at second precision; not ownership or permissions")
	skipExisting := f.Bool("skip-existing", false, "allow existing directories and skip existing regular files without comparing content")
	bwlimit := f.Int64("bwlimit", 0, "operation-wide bytes/sec; 0 unlimited; includes hash and resume reads, excludes metadata")
	filesFrom := f.String("files-from", "", "read selected relative regular-file paths from a local file; - reads stdin")
	from0 := f.Bool("from0", false, "split files-from records on NUL instead of LF/CRLF")
	var exclude transferExclusions
	f.Var(&exclude, "exclude", "exclude a relative slash glob or basename; repeatable, no **")
	if err := parseFlags(f, args); err != nil {
		return err
	}
	positional := f.Args()
	if len(positional) != 3 || *timeout <= 0 {
		return fmt.Errorf("usage: shellpier %s [--interface NAME] [--timeout 5m] [--json] TICKET SOURCE DESTINATION", command)
	}
	streamUpload := command == "upload" && positional[1] == "-"
	streamDownload := command == "download" && positional[2] == "-"
	streaming := streamUpload || streamDownload
	listMode := *filesFrom != ""
	f.Visit(func(flag *flag.Flag) {
		if flag.Name == "files-from" {
			listMode = true
		}
	})
	if listMode && *filesFrom == "" {
		return &UsageError{Err: errors.New("files-from requires a nonempty filename or -")}
	}
	if *from0 && !listMode {
		return &UsageError{Err: errors.New("from0 requires files-from")}
	}
	if listMode && (streaming || *recursive || *resume || *resumeState != "" || len(exclude) != 0) {
		return &UsageError{Err: errors.New("files-from cannot use payload streams, recursive, exclude, or resume")}
	}
	if streaming && (*recursive || *resume || *resumeState != "" || *preserveTimes || *dryRun || *skipExisting) {
		return &UsageError{Err: errors.New("stream transfers cannot use recursive, resume, preserve-times, dry-run, or skip-existing")}
	}
	if streamUpload && stdin == nil {
		return errors.New("upload from - requires a standard input stream")
	}
	if streamDownload && *asJSON {
		return &UsageError{Err: errors.New("download to - writes raw bytes and cannot use --json")}
	}
	for _, value := range positional[1:] {
		if value == "" || strings.ContainsRune(value, 0) {
			return errors.New("source and destination paths must be nonempty and contain no NUL")
		}
	}
	if *maxFiles < 1 || *maxFiles > 100000 || *maxDepth < 0 || *maxDepth > 128 || *recursive && (*resume || *resumeState != "") || *resumeState != "" && !*resume || *progressMode != "" && *progressMode != "jsonl" || len(exclude) > 0 && !*recursive {
		return errors.New("invalid recursive/resume/progress options or capacity limits")
	}
	if *bwlimit < 0 {
		return errors.New("bwlimit must be nonnegative integer bytes/sec")
	}
	if *resume && (*dryRun || *skipExisting) {
		return errors.New("resume cannot be combined with dry-run or skip-existing")
	}
	if err := retry.Validate(); err != nil {
		return err
	}
	parent = retry.Context(parent)
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	var selected []string
	if listMode {
		var listErr error
		selected, listErr = readTransferList(ctx, *filesFrom, stdin, *from0, *maxFiles, *maxDepth)
		if listErr != nil {
			return listErr
		}
	}
	ticket, err := access.Read(positional[0])
	if err != nil {
		return err
	}
	if err = ticket.Validate("access"); err != nil {
		return err
	}
	options := transferOptions{dryRun: *dryRun, preserveTimes: *preserveTimes, skipExisting: *skipExisting}
	if *bwlimit > 0 {
		options.budget = &transferBudget{rate: *bwlimit}
	}
	ctx = context.WithValue(ctx, transferOptionsKey{}, options)
	client, err := access.DialRetry(ctx, ticket, *iface, retry.RetryOptions)
	if err != nil {
		return err
	}
	defer client.Close()
	if streaming {
		streamCtx, stopStream := context.WithCancel(ctx)
		defer stopStream()
		ctx = streamCtx
		go func() {
			_ = client.Wait()
			stopStream()
		}()
	}
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
	case listMode:
		result, err = runTransferList(ctx, src, dst, positional[1], positional[2], selected, progress)
	case streamUpload:
		result.Bytes, err = uploadStream(ctx, sf, stdin, positional[2], progress)
		result.Files = 1
	case streamDownload:
		_, err = downloadStream(ctx, sf, positional[1], stdout, progress)
		return err
	case *recursive:
		result, err = runTransferTree(ctx, src, dst, positional[1], positional[2], exclude, *maxFiles, *maxDepth, progress)
	case *resume:
		var original os.FileInfo
		original, err = src.lstat(positional[1])
		if err != nil {
			break
		}
		result, err = resumeTransfer(ctx, src, dst, positional[1], positional[2], *resumeState, command, ticket.HostKey, progress)
		if err == nil {
			if *preserveTimes {
				now, e := src.lstat(positional[1])
				if e != nil || !sameTransferSource(original, now) {
					err = errors.New("source changed before mtime preservation")
					break
				}
				err = preserveTransferTime(ctx, dst, positional[2], original)
				if err == nil {
					result.TimesPreserved = 1
				}
			}
		}
	default:
		result, err = runSingleTransfer(ctx, src, dst, positional[1], positional[2], progress)

	}
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	result.Command = command
	if *preserveTimes {
		result.MtimePrecision = "seconds"
	}
	if result.Warnings == nil {
		result.Warnings = []string{}
	}
	if *asJSON {
		return json.NewEncoder(stdout).Encode(result)
	}
	_, err = fmt.Fprintf(stdout, "Transferred %d bytes in %d files and %d directories; skipped %d files; dry-run=%t.\n", result.Bytes, result.Files, result.Directories, result.SkippedFiles, result.DryRun)
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
