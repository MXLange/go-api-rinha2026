package main

import (
	"errors"
	"log"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

var badGateway = []byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	bind := getenv("BIND_ADDR", "")
	if bind == "" {
		bind = ":" + getenv("PORT", "9999")
	}

	upstreams := parseCSV(os.Getenv("FD_UPSTREAMS"))
	if len(upstreams) == 0 {
		upstreams = []string{"/run/sock/api1.sock", "/run/sock/api2.sock"}
	}

	workers := getenvInt("WORKERS", runtime.GOMAXPROCS(0))
	if workers < 1 {
		workers = 1
	}

	waitUpstreams(upstreams, 5*time.Second)

	listener, err := net.Listen("tcp", bind)
	if err != nil {
		log.Fatalf("listen %s: %v", bind, err)
	}
	defer listener.Close()

	tcpListener, ok := listener.(*net.TCPListener)
	if !ok {
		log.Fatalf("listener is not tcp")
	}

	lb := &loadBalancer{upstreams: upstreams}
	log.Printf("ready bind=%s upstreams=%s workers=%d gomaxprocs=%d gomemlimit=%s",
		bind, strings.Join(upstreams, ","), workers, runtime.GOMAXPROCS(0), os.Getenv("GOMEMLIMIT"))

	for i := 0; i < workers; i++ {
		go lb.acceptLoop(tcpListener)
	}
	select {}
}

type loadBalancer struct {
	next      atomic.Uint64
	upstreams []string
}

func (lb *loadBalancer) acceptLoop(listener *net.TCPListener) {
	for {
		conn, err := listener.AcceptTCP()
		if err != nil {
			continue
		}
		_ = conn.SetNoDelay(true)
		_ = conn.SetKeepAlive(true)
		_ = conn.SetKeepAlivePeriod(30 * time.Second)

		if err := lb.pass(conn); err != nil {
			_, _ = conn.Write(badGateway)
		}
		_ = conn.Close()
	}
}

func (lb *loadBalancer) pass(conn *net.TCPConn) error {
	start := int(lb.next.Add(1)-1) % len(lb.upstreams)
	var lastErr error
	for i := 0; i < len(lb.upstreams); i++ {
		upstream := lb.upstreams[(start+i)%len(lb.upstreams)]
		if err := sendConn(upstream, conn); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("no upstreams")
	}
	return lastErr
}

func sendConn(sockPath string, conn *net.TCPConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return err
	}

	fd := -1
	if err := raw.Control(func(rawfd uintptr) {
		fd = int(rawfd)
	}); err != nil {
		return err
	}
	if fd < 0 {
		return errors.New("invalid tcp fd")
	}

	control, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sockPath, Net: "unix"})
	if err != nil {
		return err
	}
	defer control.Close()

	rights := syscall.UnixRights(fd)
	_, _, err = control.WriteMsgUnix([]byte{0}, rights, nil)
	return err
}

func waitUpstreams(upstreams []string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for {
		ready := 0
		for _, upstream := range upstreams {
			control, err := net.DialTimeout("unix", upstream, 100*time.Millisecond)
			if err == nil {
				ready++
				_ = control.Close()
			}
		}
		if ready == len(upstreams) {
			return
		}
		if time.Now().After(deadline) {
			log.Printf("continuing with %d/%d upstream sockets ready", ready, len(upstreams))
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func parseCSV(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func getenv(name string, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}

func getenvInt(name string, fallback int) int {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
