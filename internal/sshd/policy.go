package sshd

import (
	"encoding/binary"
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"
)

// AccessMode restricts authenticated SSH capabilities, not filesystem visibility.
type AccessMode string

const (
	AccessFull         AccessMode = "full"
	AccessSFTP         AccessMode = "sftp"
	AccessSFTPReadOnly AccessMode = "sftp-readonly"
)

func ParseAccessMode(value string) (AccessMode, error) {
	switch AccessMode(value) {
	case "", AccessFull:
		return AccessFull, nil
	case AccessSFTP:
		return AccessSFTP, nil
	case AccessSFTPReadOnly:
		return AccessSFTPReadOnly, nil
	default:
		return "", fmt.Errorf("invalid access mode: expected full, sftp, or sftp-readonly")
	}
}
func (s *Server) fullAccess() bool { return s.AccessMode == "" || s.AccessMode == AccessFull }
func (s *Server) accessMode() AccessMode {
	if s.AccessMode == "" {
		return AccessFull
	}
	return s.AccessMode
}

// readOnlySFTPChannel blocks extension writes that the dependency's ReadOnly
// option misclassifies (notably hardlink@openssh.com). Unknown extensions are
// returned as SSH_FX_OP_UNSUPPORTED by the SFTP server, preserving the session.
type readOnlySFTPChannel struct {
	ssh.Channel
	pending []byte
}

func (c *readOnlySFTPChannel) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(c.pending) == 0 {
		var header [4]byte
		if _, err := io.ReadFull(c.Channel, header[:]); err != nil {
			return 0, err
		}
		size := binary.BigEndian.Uint32(header[:])
		if size == 0 || size > 1<<20 {
			return 0, fmt.Errorf("invalid SFTP packet size")
		}
		packet := make([]byte, 4+int(size))
		copy(packet, header[:])
		if _, err := io.ReadFull(c.Channel, packet[4:]); err != nil {
			return 0, err
		}
		// EXTENDED: type, request ID, extension string length, extension string.
		if packet[4] == 200 && len(packet) >= 13 {
			length := binary.BigEndian.Uint32(packet[9:13])
			if length > 0 && uint64(length) <= uint64(len(packet)-13) && string(packet[13:13+int(length)]) != "statvfs@openssh.com" {
				packet[13] = 0
			}
		}
		// The dependency checks WRITE but independently honors CREAT/TRUNC.
		// Mark every OPEN except exactly READ as writable so ReadOnly rejects it.
		if packet[4] == 3 && len(packet) >= 13 {
			length := binary.BigEndian.Uint32(packet[9:13])
			if uint64(length)+17 <= uint64(len(packet)) {
				offset := 13 + int(length)
				flags := binary.BigEndian.Uint32(packet[offset : offset+4])
				if flags != 1 {
					binary.BigEndian.PutUint32(packet[offset:offset+4], flags|2)
				}
			}
		}

		c.pending = packet
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}
