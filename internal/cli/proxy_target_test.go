package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestProxyTargetBinaryHalfClose(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	payload := []byte{0, 255, 128, '\r', '\n', 1}
	served := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		defer conn.Close()
		data, err := io.ReadAll(conn)
		if err == nil && !bytes.Equal(data, payload) {
			err = errors.New("input bytes changed")
		}
		if err == nil {
			_, err = conn.Write(append([]byte("after EOF:"), data...))
		}
		served <- err
	}()
	ticket := forwardTicket(t, context.Background(), []string{listener.Addr().String()}, time.Minute)
	var out, diagnostic bytes.Buffer
	err = Run(context.Background(), []string{"proxy", ticket, "--to", listener.Addr().String()}, bytes.NewReader(payload), &out, &diagnostic, "test")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), append([]byte("after EOF:"), payload...)) || diagnostic.Len() != 0 {
		t.Fatalf("output %q; stderr %q", out.Bytes(), diagnostic.String())
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}

func TestProxyTargetLifetime(t *testing.T) {
	for _, mode := range []string{"cancel", "timeout", "expiry", "loss"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			accepted := make(chan struct{})
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				close(accepted)
				_, _ = io.Copy(io.Discard, conn)
			}()
			hostCtx, stopHost := context.WithCancel(context.Background())
			defer stopHost()
			lifetime := time.Minute
			if mode == "expiry" {
				lifetime = 1500 * time.Millisecond
			}
			ticket := forwardTicket(t, hostCtx, []string{listener.Addr().String()}, lifetime)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reader, writer := io.Pipe()
			input := &proxyObservedInput{PipeReader: reader, started: make(chan struct{}), released: make(chan struct{})}
			defer writer.Close()
			args := []string{"proxy", ticket, "--to", listener.Addr().String()}
			if mode == "timeout" {
				args = append(args, "--timeout", "300ms")
			}
			done := make(chan error, 1)
			go func() { done <- Run(ctx, args, input, io.Discard, io.Discard, "test") }()
			select {
			case <-accepted:
			case <-time.After(3 * time.Second):
				t.Fatal("target not connected")
			}
			select {
			case <-input.started:
			case <-time.After(time.Second):
				t.Fatal("input read not started")
			}
			if mode == "cancel" {
				cancel()
			}
			if mode == "loss" {
				stopHost()
			}
			select {
			case err := <-done:
				if mode == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancel: %v", err)
				}
				if (mode == "expiry" || mode == "timeout") && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("deadline: %v", err)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("proxy did not terminate")
			}
			select {
			case <-input.released:
			case <-time.After(time.Second):
				t.Fatal("input read stayed blocked after proxy exit")
			}
		})
	}
}

func TestProxyTargetRefused(t *testing.T) {
	ticket := forwardTicket(t, context.Background(), []string{"127.0.0.1:1"}, time.Minute)
	var out bytes.Buffer
	err := Run(context.Background(), []string{"proxy", ticket, "--to", "127.0.0.1:2"}, strings.NewReader("secret"), &out, io.Discard, "test")
	if err == nil || out.Len() != 0 {
		t.Fatalf("refusal: %v, %q", err, out.String())
	}
}

func TestProxyTargetBadFlags(t *testing.T) {
	cases := [][]string{
		{"--to", ""}, {"--to", "localhost:0"}, {"--to", "localhost:80", "--timeout", "-1s"},
		{"--to", "localhost:80", "--retries", "-1"}, {"--timeout", "1s"}, {"--json"},
	}
	for _, args := range cases {
		args = append([]string{"proxy", "@missing-ticket"}, args...)
		var out bytes.Buffer
		err := Run(context.Background(), args, nil, &out, io.Discard, "test")
		if err == nil || strings.Contains(err.Error(), "missing-ticket") || out.Len() != 0 {
			t.Fatalf("%v: %v, %q", args, err, out.String())
		}
	}
}

func TestProxyLegacyHelpAndTicketValidation(t *testing.T) {
	var out bytes.Buffer
	if err := Run(context.Background(), []string{"proxy", "@missing-ticket", "--help"}, nil, io.Discard, &out, "test"); err != nil || !strings.Contains(out.String(), "interface") {
		t.Fatalf("offline help: %v, %q", err, out.String())
	}
	err := Run(context.Background(), []string{"proxy", "@missing-ticket"}, nil, io.Discard, io.Discard, "test")
	if err == nil || !strings.Contains(err.Error(), "missing-ticket") {
		t.Fatalf("legacy ticket read: %v", err)
	}
}

// The fixture observes the actual blocked Read; the test never closes its reader.
type proxyObservedInput struct {
	*io.PipeReader
	started  chan struct{}
	released chan struct{}
}

func (r *proxyObservedInput) Read(p []byte) (int, error) {
	close(r.started)
	n, err := r.PipeReader.Read(p)
	close(r.released)
	return n, err
}

func TestProxyTargetRemoteHalfClosePreservesUpload(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	halfClosed := make(chan struct{})
	received := make(chan []byte, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.(*net.TCPConn).CloseWrite()
		close(halfClosed)
		data, _ := io.ReadAll(conn)
		received <- data
	}()
	ticket := forwardTicket(t, context.Background(), []string{listener.Addr().String()}, time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	input, writer := io.Pipe()
	defer writer.Close()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, []string{"proxy", ticket, "--to", listener.Addr().String()}, input, io.Discard, io.Discard, "test")
	}()
	<-halfClosed
	time.Sleep(50 * time.Millisecond)
	payload := []byte("delayed upload after downstream EOF")
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if data := <-received; !bytes.Equal(data, payload) {
		t.Fatalf("upload truncated: %q", data)
	}
}

func TestProxyTargetCancelBlockedOutput(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("response"))
		_, _ = io.Copy(io.Discard, conn)
	}()
	ticket := forwardTicket(t, context.Background(), []string{listener.Addr().String()}, time.Minute)
	output, writer := io.Pipe()
	defer output.Close()
	observed := &proxyObservedOutput{PipeWriter: writer, started: make(chan struct{}), released: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, []string{"proxy", ticket, "--to", listener.Addr().String()}, strings.NewReader(""), observed, io.Discard, "test")
	}()
	select {
	case <-observed.started:
	case <-time.After(3 * time.Second):
		t.Fatal("output write not started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("proxy stuck in output")
	}
	select {
	case <-observed.released:
	case <-time.After(time.Second):
		t.Fatal("output write stayed blocked")
	}
}

type proxyObservedOutput struct {
	*io.PipeWriter
	started  chan struct{}
	released chan struct{}
}

func (w *proxyObservedOutput) Write(p []byte) (int, error) {
	close(w.started)
	n, err := w.PipeWriter.Write(p)
	close(w.released)
	return n, err
}

func TestProxyTargetLossAfterInputEOF(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	uploadEOF := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(io.Discard, conn)
		close(uploadEOF)
		<-release
	}()
	hostCtx, stopHost := context.WithCancel(context.Background())
	defer stopHost()
	ticket := forwardTicket(t, hostCtx, []string{listener.Addr().String()}, time.Minute)
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), []string{"proxy", ticket, "--to", listener.Addr().String()}, strings.NewReader(""), io.Discard, io.Discard, "test")
	}()
	select {
	case <-uploadEOF:
	case <-time.After(3 * time.Second):
		t.Fatal("backend did not observe upload EOF")
	}
	stopHost()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("SSH loss reported successful EOF")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("proxy did not report SSH loss")
	}
}
