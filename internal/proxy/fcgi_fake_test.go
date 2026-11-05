//go:build integration

package proxy

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

const (
	fcgiStdout     = 6
	fcgiStdin      = 5
	fcgiEndRequest = 3
)

// serveFCGI answers one FastCGI request with body, which stands in for
// the output of phpversion().
func serveFCGI(conn net.Conn, body string) (int, error) {
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	for {
		id, typ, payload, err := readFCGI(conn)
		if err != nil {
			return 0, err
		}
		// An empty STDIN record ends the request.
		if typ == fcgiStdin && len(payload) == 0 {
			if err := writeFCGIResponse(conn, id, body); err != nil {
				return 0, err
			}
			return 1, nil
		}
	}
}

func writeFCGIResponse(w io.Writer, id uint16, body string) error {
	payload := "Status: 200\r\nContent-Type: text/plain\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
	if err := writeFCGI(w, fcgiStdout, id, []byte(payload)); err != nil {
		return err
	}
	end := make([]byte, 8)
	return writeFCGI(w, fcgiEndRequest, id, end)
}

func readFCGI(r io.Reader) (id uint16, typ byte, body []byte, err error) {
	var hdr [8]byte
	if _, err = io.ReadFull(r, hdr[:]); err != nil {
		return 0, 0, nil, err
	}
	if hdr[0] != 1 {
		return 0, 0, nil, fmt.Errorf("fcgi version %d", hdr[0])
	}
	typ = hdr[1]
	id = binary.BigEndian.Uint16(hdr[2:4])
	n := int(binary.BigEndian.Uint16(hdr[4:6]))
	pad := int(hdr[6])
	body = make([]byte, n)
	if n > 0 {
		if _, err = io.ReadFull(r, body); err != nil {
			return 0, 0, nil, err
		}
	}
	if pad > 0 {
		if _, err = io.CopyN(io.Discard, r, int64(pad)); err != nil {
			return 0, 0, nil, err
		}
	}
	return id, typ, body, nil
}

func writeFCGI(w io.Writer, typ byte, id uint16, body []byte) error {
	var hdr [8]byte
	hdr[0] = 1
	hdr[1] = typ
	binary.BigEndian.PutUint16(hdr[2:4], id)
	binary.BigEndian.PutUint16(hdr[4:6], uint16(len(body)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(body)
	return err
}
