// Package frr provides a client for configuring FRR daemons via the VTY
// Unix socket protocol. Each FRR daemon (bgpd, ospfd, bfdd, zebra) exposes
// a .vty socket that accepts CLI commands with a binary framing protocol.
package frr

import (
	"bytes"
	"fmt"
	"net"
	"time"
)

// VTY command return codes from FRR (lib/command.h).
const (
	cmdSuccess       = 0
	cmdWarning       = 1
	cmdErrNoMatch    = 2
	cmdErrAmbiguous  = 3
	cmdErrIncomplete = 4
)

// vtyConn is a single connection to an FRR daemon's VTY Unix socket.
// The protocol uses null-terminated commands and 4-byte status markers.
type vtyConn struct {
	conn    net.Conn
	timeout time.Duration
}

// dialVTY connects to an FRR daemon's VTY socket and enters enable mode.
func dialVTY(socketPath string, timeout time.Duration) (*vtyConn, error) {
	conn, err := net.DialTimeout("unix", socketPath, timeout)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", socketPath, err)
	}

	vc := &vtyConn{conn: conn, timeout: timeout}

	// Set deadline for the initial banner read.
	conn.SetDeadline(time.Now().Add(timeout))

	// Read initial banner/prompt from daemon.
	if _, _, err := vc.readResponse(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("read banner from %s: %w", socketPath, err)
	}

	// Enter privileged mode.
	if _, status, err := vc.execCmd("enable"); err != nil {
		conn.Close()
		return nil, fmt.Errorf("enable on %s: %w", socketPath, err)
	} else if status != cmdSuccess {
		conn.Close()
		return nil, fmt.Errorf("enable failed on %s (status=%d)", socketPath, status)
	}

	return vc, nil
}

// execCmd sends a command and returns the output and status code.
func (vc *vtyConn) execCmd(cmd string) (string, byte, error) {
	vc.conn.SetDeadline(time.Now().Add(vc.timeout))

	// Write command + null terminator.
	if _, err := vc.conn.Write(append([]byte(cmd), 0)); err != nil {
		return "", 0, fmt.Errorf("write %q: %w", cmd, err)
	}

	return vc.readResponse()
}

// readResponse reads until the VTY end marker is found.
// FRR VTY protocol: after output, daemon sends a 4-byte trailer:
//
//	\x00 <status_byte> \x00 \x00
//
// where status_byte is CMD_SUCCESS (0), CMD_WARNING (1), etc.
func (vc *vtyConn) readResponse() (string, byte, error) {
	var buf bytes.Buffer
	tmp := make([]byte, 4096)

	for {
		n, err := vc.conn.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}

		// Check for end marker in accumulated data.
		data := buf.Bytes()
		if len(data) >= 4 {
			end := data[len(data)-4:]
			if end[0] == 0 && end[2] == 0 && end[3] == 0 {
				output := string(data[:len(data)-4])
				status := end[1]
				return output, status, nil
			}
		}

		if err != nil {
			return buf.String(), 0, err
		}
	}
}

// close disconnects from the VTY socket.
func (vc *vtyConn) close() error {
	return vc.conn.Close()
}
