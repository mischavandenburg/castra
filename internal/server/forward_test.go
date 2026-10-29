package server

import (
	"bufio"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// echo serves ln by sending back the first line it reads, prefixed with tag,
// until ln is closed.
func echo(ln net.Listener, tag string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer c.Close()
			line, _ := bufio.NewReader(c).ReadString('\n')
			_, _ = io.WriteString(c, tag+line)
		}()
	}
}

// roundTrip sends a line over c and returns the line that comes back.
func roundTrip(t *testing.T, c net.Conn) string {
	t.Helper()
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(c, "ping\n"); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatalf("no reply through the forward: %v", err)
	}
	return line
}

// TestLocalForward is `ssh -L`: the server dials the destination from inside
// the container.
func TestLocalForward(t *testing.T) {
	h, err := start(t)
	if err != nil {
		t.Fatal(err)
	}
	dest, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()
	go echo(dest, "inside:")

	c, err := h.client.Dial("tcp", dest.Addr().String())
	if err != nil {
		t.Fatalf("direct-tcpip refused: %v", err)
	}
	if got := roundTrip(t, c); got != "inside:ping\n" {
		t.Fatalf("got %q", got)
	}
	if _, err := h.client.Dial("tcp", "127.0.0.1:1"); err == nil {
		t.Fatal("a forward to a closed port should fail")
	}
}

// TestRemoteForward is `ssh -R`: the server listens inside the container and
// sends each connection back to the client. Cancelling the forward, and
// ending the connection, both close the listener.
func TestRemoteForward(t *testing.T) {
	h, err := start(t)
	if err != nil {
		t.Fatal(err)
	}
	remote, err := h.client.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcpip-forward refused: %v", err)
	}
	go echo(remote, "client:")
	addr := remote.Addr().String() // the port the server bound for us

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	if got := roundTrip(t, c); got != "client:ping\n" {
		t.Fatalf("got %q", got)
	}

	_ = remote.Close() // cancel-tcpip-forward
	waitFor(t, func() bool { return !reachable("tcp", addr) }, "the cancelled forward's listener %s to close", addr)

	again, err := h.client.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr = again.Addr().String()
	_ = h.client.Close()
	waitFor(t, func() bool { return !reachable("tcp", addr) }, "the listener %s to close with its connection", addr)
}

// TestUnixForwards covers both Unix-socket forwards: `ssh -L` and `ssh -R`
// with a socket path on the container's side.
func TestUnixForwards(t *testing.T) {
	h, err := start(t)
	if err != nil {
		t.Fatal(err)
	}
	// Short paths: a Unix socket path is limited to about 100 bytes, and
	// macOS temp dirs are long.
	dir, err := os.MkdirTemp("/tmp", "castra-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	dest, err := net.Listen("unix", filepath.Join(dir, "dest.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer dest.Close()
	go echo(dest, "inside:")
	c, err := h.client.Dial("unix", filepath.Join(dir, "dest.sock"))
	if err != nil {
		t.Fatalf("direct-streamlocal refused: %v", err)
	}
	if got := roundTrip(t, c); got != "inside:ping\n" {
		t.Fatalf("local unix forward: got %q", got)
	}

	path := filepath.Join(dir, "remote.sock")
	if err := os.WriteFile(path, nil, 0o600); err != nil { // a stale file is replaced
		t.Fatal(err)
	}
	remote, err := h.client.ListenUnix(path)
	if err != nil {
		t.Fatalf("streamlocal-forward refused: %v", err)
	}
	go echo(remote, "client:")
	c, err = net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if got := roundTrip(t, c); got != "client:ping\n" {
		t.Fatalf("remote unix forward: got %q", got)
	}
	_ = remote.Close()
	waitFor(t, func() bool { return !reachable("unix", path) }, "the unix forward %s to close", path)
}

func reachable(network, addr string) bool {
	c, err := net.DialTimeout(network, addr, time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}
