package cli

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/pkg/sftp"
)

// streamInput keeps at most one fixed-size read in flight. Closing a pipe on
// cancellation interrupts its pending read; an arbitrary non-closable Reader
// must eventually return from Read to release its single reader goroutine.
type streamReadResult struct {
	data []byte
	err  error
}

type streamInput struct {
	ctx      context.Context
	requests chan int
	results  chan streamReadResult
	stop     chan struct{}
}

func newStreamInput(ctx context.Context, input io.Reader) *streamInput {
	r := &streamInput{ctx, make(chan int), make(chan streamReadResult), make(chan struct{})}
	go func() {
		buf := make([]byte, 64*1024)
		for {
			var size int
			select {
			case size = <-r.requests:
			case <-ctx.Done():
				return
			case <-r.stop:
				return
			}
			n, err := input.Read(buf[:size])
			select {
			case r.results <- streamReadResult{buf[:n], err}:
			case <-ctx.Done():
				return
			case <-r.stop:
				return
			}
		}
	}()
	return r
}

func (r *streamInput) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	size := min(len(p), 64*1024)
	select {
	case r.requests <- size:
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	}
	select {
	case value := <-r.results:
		return copy(p, value.data), value.err
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	}
}

func uploadStream(ctx context.Context, sf *sftp.Client, input io.Reader, destination string, progress func(int64) error) (int64, error) {
	output, err := sf.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, incompleteUpload(destination, errors.Join(err, ctx.Err()))
		}
		return 0, err
	}
	// Cancellation also releases a caller's pipe blocked waiting for input.
	stop := context.AfterFunc(ctx, func() {
		if closer, ok := input.(io.Closer); ok {
			_ = closer.Close()
		}
	})
	defer stop()
	reader := newStreamInput(ctx, input)
	defer close(reader.stop)
	count, copyErr := transferCopy(ctx, output, reader, progress)
	closeErr := output.Close()
	if err = errors.Join(copyErr, closeErr, ctx.Err()); err != nil {
		return count, incompleteUpload(destination, err)
	}
	return count, nil
}

func downloadStream(ctx context.Context, sf *sftp.Client, source string, output io.Writer, progress func(int64) error) (int64, error) {
	info, err := sf.Lstat(source)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("stream source must be a regular file, not a symlink")
	}
	input, err := sf.Open(source)
	if err != nil {
		return 0, err
	}
	defer input.Close()
	actual, err := input.Stat()
	if err != nil {
		return 0, err
	}
	if !actual.Mode().IsRegular() {
		return 0, errors.New("opened stream source is not a regular file")
	}
	// A downstream consumer may stop reading while this copy is blocked in
	// Write. Close cancellable outputs so cancellation or SSH loss releases it.
	// Non-closable writers must return from Write themselves; no background
	// copy is started that could accumulate blocked writer goroutines.
	stop := context.AfterFunc(ctx, func() {
		if closer, ok := output.(io.Closer); ok {
			_ = closer.Close()
		}
	})
	defer stop()
	count, copyErr := transferCopy(ctx, output, input, progress)
	return count, errors.Join(copyErr, input.Close(), ctx.Err())
}
