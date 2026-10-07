package main

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// Carbide Motion's "Allow Remote Access" listener. The protocol is not
// documented by Carbide 3D; this follows what Carbide Create sends, as worked
// out by the send-carbide project:
//
//	recv  STATE: init
//	send  GCODE: <name>:<size in bytes>\n
//	send  <size bytes of G-code>\n
//	recv  GCODE_ACK
const carbideDefaultPort = "6280"

// SendCarbide delivers one G-code program to Carbide Motion and waits for its
// acknowledgement. The name must not contain a colon or a newline.
func SendCarbide(addr, name string, body []byte, timeout time.Duration) error {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, carbideDefaultPort)
	}
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("cannot reach Carbide Motion at %s (is it running, with Allow Remote Access on?): %w", addr, err)
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)

	state, err := readMessage(conn, deadline)
	if err != nil {
		return fmt.Errorf("no greeting from Carbide Motion: %w", err)
	}
	fields := strings.Fields(state)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "STATE:") {
		return fmt.Errorf("unexpected greeting from Carbide Motion: %q", state)
	}
	if !strings.EqualFold(fields[1], "init") {
		return fmt.Errorf("Carbide Motion is not ready to receive a file (state %q)", fields[1])
	}

	conn.SetWriteDeadline(deadline)
	header := fmt.Sprintf("GCODE: %s:%d\n", name, len(body))
	if _, err := conn.Write([]byte(header)); err != nil {
		return fmt.Errorf("sending header: %w", err)
	}
	if _, err := conn.Write(body); err != nil {
		return fmt.Errorf("sending G-code: %w", err)
	}
	if _, err := conn.Write([]byte{'\n'}); err != nil {
		return fmt.Errorf("sending terminator: %w", err)
	}

	ack, err := readMessage(conn, deadline)
	if err != nil {
		return fmt.Errorf("file sent but Carbide Motion did not acknowledge it: %w", err)
	}
	if strings.TrimSpace(ack) != "GCODE_ACK" {
		return fmt.Errorf("file sent but Carbide Motion answered %q instead of GCODE_ACK", ack)
	}
	return nil
}

// readMessage reads one short newline-terminated message. Carbide Motion's
// messages are tiny, and it is not certain every build ends them with a
// newline, so once some data has arrived a short quiet period also ends the
// message.
func readMessage(conn net.Conn, deadline time.Time) (string, error) {
	var msg []byte
	buf := make([]byte, 128)
	conn.SetReadDeadline(deadline)
	for {
		n, err := conn.Read(buf)
		msg = append(msg, buf[:n]...)
		if i := strings.IndexByte(string(msg), '\n'); i >= 0 {
			return strings.TrimSpace(string(msg[:i])), nil
		}
		if err != nil {
			var ne net.Error
			if len(msg) > 0 && errors.As(err, &ne) && ne.Timeout() {
				return strings.TrimSpace(string(msg)), nil
			}
			if len(msg) > 0 {
				return strings.TrimSpace(string(msg)), nil
			}
			return "", err
		}
		if len(msg) > 512 {
			return "", errors.New("oversized message")
		}
		quiet := time.Now().Add(300 * time.Millisecond)
		if quiet.After(deadline) {
			quiet = deadline
		}
		conn.SetReadDeadline(quiet)
	}
}
