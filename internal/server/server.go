package server

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"go-api-rinha2026/internal/response"
)

const (
	RxCap = 8192
)

type FraudHandler func(body []byte) []byte

type parsedKind uint8

const (
	parsedIncomplete parsedKind = iota
	parsedBad
	parsedReady
	parsedNotFound
	parsedFraud
)

type parsedRequest struct {
	kind      parsedKind
	bodyStart int
	bodyEnd   int
	consumed  int
}

func ServeFD(sockPath string, handler FraudHandler) error {
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o755); err != nil {
		return err
	}
	if err := os.Remove(sockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	addr := net.UnixAddr{Name: sockPath, Net: "unix"}
	listener, err := net.ListenUnix("unix", &addr)
	if err != nil {
		return err
	}
	defer listener.Close()
	_ = os.Chmod(sockPath, 0o666)

	for {
		control, err := listener.AcceptUnix()
		if err != nil {
			continue
		}
		fd, err := recvFD(control)
		_ = control.Close()
		if err != nil {
			continue
		}

		if err := syscall.SetNonblock(fd, false); err != nil {
			_ = syscall.Close(fd)
			continue
		}
		_ = syscall.SetsockoptInt(fd, syscall.IPPROTO_TCP, syscall.TCP_NODELAY, 1)
		go serveFD(fd, handler)
	}
}

func recvFD(conn *net.UnixConn) (int, error) {
	var one [1]byte
	oob := make([]byte, 64)
	_, oobn, _, _, err := conn.ReadMsgUnix(one[:], oob)
	if err != nil {
		return -1, err
	}
	msgs, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return -1, err
	}
	for _, msg := range msgs {
		fds, err := syscall.ParseUnixRights(&msg)
		if err == nil && len(fds) > 0 {
			return fds[0], nil
		}
	}
	return -1, errors.New("missing fd")
}

func serveFD(fd int, handler FraudHandler) {
	defer syscall.Close(fd)

	rx := make([]byte, RxCap)
	head := 0
	tail := 0

	for {
		for head < tail {
			req := parseRequest(rx[head:tail])
			switch req.kind {
			case parsedIncomplete:
				goto readMore
			case parsedBad:
				_ = writeAllFD(fd, response.BadReq)
				return
			case parsedReady:
				if !writeAllFD(fd, response.Ready) {
					return
				}
				head += req.consumed
			case parsedNotFound:
				if !writeAllFD(fd, response.NotFound) {
					return
				}
				head += req.consumed
			case parsedFraud:
				resp := handler(rx[head+req.bodyStart : head+req.bodyEnd])
				if !writeAllFD(fd, resp) {
					return
				}
				head += req.consumed
			}
		}

	readMore:
		if head == tail {
			head = 0
			tail = 0
		} else if tail == len(rx) && head > 0 {
			copy(rx, rx[head:tail])
			tail -= head
			head = 0
		}
		if tail == len(rx) {
			return
		}

		n, err := readFD(fd, rx[tail:])
		if err != nil || n == 0 {
			return
		}
		tail += n
	}
}

func readFD(fd int, payload []byte) (int, error) {
	for {
		n, err := syscall.Read(fd, payload)
		if err == syscall.EINTR {
			continue
		}
		return n, err
	}
}

func writeAllFD(fd int, payload []byte) bool {
	for len(payload) > 0 {
		n, err := syscall.Write(fd, payload)
		if err == syscall.EINTR {
			continue
		}
		if err != nil || n <= 0 {
			return false
		}
		payload = payload[n:]
	}
	return true
}

func parseRequest(buf []byte) parsedRequest {
	if len(buf) < 16 {
		return parsedRequest{kind: parsedIncomplete}
	}

	headerEnd := bytes.Index(buf, []byte("\r\n\r\n"))
	if headerEnd < 0 {
		return parsedRequest{kind: parsedIncomplete}
	}

	lineEnd := bytes.IndexByte(buf[:headerEnd], '\r')
	if lineEnd < 0 {
		return parsedRequest{kind: parsedBad}
	}
	line := buf[:lineEnd]

	if bytes.HasPrefix(line, []byte("POST ")) {
		rest := line[5:]
		if pathEq(rest, []byte("/fraud-score")) {
			contentLength := parseContentLength(buf[lineEnd:headerEnd])
			bodyStart := headerEnd + 4
			bodyEnd := bodyStart + contentLength
			if len(buf) < bodyEnd {
				return parsedRequest{kind: parsedIncomplete}
			}
			return parsedRequest{kind: parsedFraud, bodyStart: bodyStart, bodyEnd: bodyEnd, consumed: bodyEnd}
		}
		return parsedRequest{kind: parsedNotFound, consumed: headerEnd + 4}
	}

	if bytes.HasPrefix(line, []byte("GET ")) {
		rest := line[4:]
		if pathEq(rest, []byte("/ready")) {
			return parsedRequest{kind: parsedReady, consumed: headerEnd + 4}
		}
		return parsedRequest{kind: parsedNotFound, consumed: headerEnd + 4}
	}

	return parsedRequest{kind: parsedBad}
}

func pathEq(rest []byte, path []byte) bool {
	if len(rest) < len(path)+1 {
		return false
	}
	if !bytes.Equal(rest[:len(path)], path) {
		return false
	}
	next := rest[len(path)]
	return next == ' ' || next == '?'
}

func parseContentLength(headers []byte) int {
	const name = "content-length:"
	for i := 0; i+len(name) <= len(headers); i++ {
		c := headers[i]
		if c != 'c' && c != 'C' {
			continue
		}
		match := true
		for j := 1; j < len(name); j++ {
			if headers[i+j]|0x20 != name[j] {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		p := i + len(name)
		for p < len(headers) && (headers[p] == ' ' || headers[p] == '\t') {
			p++
		}
		value := 0
		for p < len(headers) && headers[p] >= '0' && headers[p] <= '9' {
			value = value*10 + int(headers[p]-'0')
			p++
		}
		return value
	}
	return 0
}
