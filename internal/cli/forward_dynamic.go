package cli

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/Boulea7/shellpier/internal/sshd"
	"golang.org/x/crypto/ssh"
)

const dynamicHandshakeTimeout = 10 * time.Second

// serveDynamicForward reads bounded SOCKS5 frames without buffering payload bytes.
func serveDynamicForward(ctx context.Context, conn net.Conn, dial func(context.Context, string, string) (net.Conn, error)) error {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if err := conn.SetDeadline(time.Now().Add(dynamicHandshakeTimeout)); err != nil {
		return err
	}
	var header [2]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return err
	}
	if header[0] != 5 {
		return errors.New("unsupported SOCKS version")
	}
	var methods [255]byte
	if _, err := io.ReadFull(conn, methods[:int(header[1])]); err != nil {
		return err
	}
	accepted := false
	for _, method := range methods[:int(header[1])] {
		if method == 0 {
			accepted = true
		}
	}
	method := byte(255)
	if accepted {
		method = 0
	}
	if err := dynamicWrite(conn, []byte{5, method}); err != nil {
		return err
	}
	if !accepted {
		return errors.New("SOCKS5 requires no-authentication method")
	}
	var request [4]byte
	if _, err := io.ReadFull(conn, request[:]); err != nil {
		return err
	}
	fail := func(code byte, err error) error { _ = dynamicReply(conn, code); return err }
	if request[0] != 5 || request[2] != 0 {
		return fail(1, errors.New("invalid SOCKS5 request"))
	}
	if request[1] != 1 {
		return fail(7, errors.New("only SOCKS5 CONNECT is supported"))
	}
	var address [255]byte
	size := 0
	switch request[3] {
	case 1:
		size = 4
	case 4:
		size = 16
	case 3:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return err
		}
		size = int(length[0])
		if size == 0 {
			return fail(8, errors.New("empty SOCKS5 domain"))
		}
	default:
		return fail(8, errors.New("unsupported SOCKS5 address type"))
	}
	if _, err := io.ReadFull(conn, address[:size]); err != nil {
		return err
	}
	host := string(address[:size])
	if request[3] != 3 {
		host = net.IP(address[:size]).String()
	} else if strings.ContainsAny(host, "\x00\r\n:/\\") {
		return fail(8, errors.New("invalid SOCKS5 domain"))
	}
	var portBytes [2]byte
	if _, err := io.ReadFull(conn, portBytes[:]); err != nil {
		return err
	}
	port := binary.BigEndian.Uint16(portBytes[:])
	if port == 0 {
		return fail(1, errors.New("SOCKS5 destination port must be positive"))
	}
	dialCtx, cancel := context.WithTimeout(ctx, sshd.DefaultForwardDialTimeout)
	remote, err := dial(dialCtx, "tcp", net.JoinHostPort(host, strconv.Itoa(int(port))))
	cancel()
	if err != nil {
		code := byte(5)
		var openErr *ssh.OpenChannelError
		if errors.As(err, &openErr) && openErr.Reason == ssh.Prohibited {
			code = 2
		}
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			code = 6
		}
		return fail(code, err)
	}
	defer remote.Close()
	// The SSH channel hides the remote socket's actual bind address; an unspecified
	// address is sufficient for CONNECT clients and avoids inventing endpoint data.
	if err := dynamicReply(conn, 0); err != nil {
		return err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return err
	}
	return sshd.BridgeForward(ctx, conn, remote)
}

func dynamicReply(out io.Writer, code byte) error {
	return dynamicWrite(out, []byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
}
func dynamicWrite(out io.Writer, data []byte) error {
	n, err := out.Write(data)
	if err == nil && n != len(data) {
		return io.ErrShortWrite
	}
	return err
}
