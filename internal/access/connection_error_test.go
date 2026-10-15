package access

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestConnectionErrorAttribution(t *testing.T) {
	transport := &connectionConn{}
	client := &ssh.Client{Conn: &trackedSSHConn{transport: transport}}
	eof := fmt.Errorf("subsystem read: %w", io.EOF)
	if ConnectionError(client, eof) != eof {
		t.Fatal("attributed a live connection")
	}
	transport.timedOut.Store(true)
	for _, err := range []error{io.EOF, io.ErrUnexpectedEOF, net.ErrClosed, io.ErrClosedPipe, &ssh.ExitMissingError{}, eof} {
		got := ConnectionError(client, err)
		if !errors.Is(got, ErrKeepaliveTimeout) || !errors.Is(got, err) {
			t.Fatalf("lost cause: %v", got)
		}
	}
	for _, err := range []error{nil, &ssh.ExitError{}, context.Canceled, context.DeadlineExceeded, os.ErrPermission, os.ErrNotExist, errors.New("invalid path"), errors.New("sftp: permission denied"), errors.Join(os.ErrPermission, io.EOF)} {
		if ConnectionError(client, err) != err {
			t.Fatalf("changed local or known result: %v", err)
		}
	}
	if ConnectionError(nil, eof) != eof {
		t.Fatal("changed untracked error")
	}
}

func TestConnectionErrorRequestAndChannelAfterBlackhole(t *testing.T) {
	ticket, _ := connectionFixture(t, false, true)
	client, err := DialWithOptions(context.Background(), ticket, "", fastConnectionOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := waitConnection(t, client); !errors.Is(err, ErrKeepaliveTimeout) {
		t.Fatal(err)
	}
	if _, _, err := client.SendRequest("probe", true, nil); !errors.Is(err, ErrKeepaliveTimeout) {
		t.Fatalf("request lost cause: %v", err)
	}
	if _, _, err := client.OpenChannel("session", nil); !errors.Is(err, ErrKeepaliveTimeout) {
		t.Fatalf("channel lost cause: %v", err)
	}
}
