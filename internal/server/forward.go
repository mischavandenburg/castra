package server

import (
	"io"
	"log/slog"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// forwards carries port forwards over one client connection in both
// directions, as DevPod's server does. The client's -L arrives as
// direct-tcpip or direct-streamlocal channels, dialled from inside the
// container. Its -R arrives as tcpip-forward or streamlocal-forward requests,
// listened for inside the container, with each accepted connection sent back
// over a forwarded channel. Remote listeners close when the connection does.
type forwards struct {
	conn *ssh.ServerConn
	log  *slog.Logger

	mu        sync.Mutex
	listeners map[string]net.Listener // "host:port", or a socket path
}

func newForwards(conn *ssh.ServerConn, log *slog.Logger) *forwards {
	return &forwards{conn: conn, log: log, listeners: map[string]net.Listener{}}
}

// local answers a -L channel: dial the destination from inside the container
// and join the two.
func (f *forwards) local(nc ssh.NewChannel, network string) {
	var addr string
	switch network {
	case "tcp":
		var p struct {
			Host     string
			Port     uint32
			OrigHost string
			OrigPort uint32
		}
		if err := ssh.Unmarshal(nc.ExtraData(), &p); err != nil {
			_ = nc.Reject(ssh.ConnectionFailed, "bad direct-tcpip request")
			return
		}
		addr = net.JoinHostPort(p.Host, strconv.FormatUint(uint64(p.Port), 10))
	case "unix":
		var p struct {
			Path      string
			Reserved0 string
			Reserved1 uint32
		}
		if err := ssh.Unmarshal(nc.ExtraData(), &p); err != nil {
			_ = nc.Reject(ssh.ConnectionFailed, "bad direct-streamlocal request")
			return
		}
		addr = p.Path
	}
	c, err := net.DialTimeout(network, addr, 10*time.Second)
	if err != nil {
		_ = nc.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, reqs, err := nc.Accept()
	if err != nil {
		_ = c.Close()
		return
	}
	go ssh.DiscardRequests(reqs)
	join(ch, c)
}

// serve answers the connection's global requests: -R forwards and their
// cancellation. Anything else, keepalives included, gets a plain "no".
func (f *forwards) serve(reqs <-chan *ssh.Request) {
	for req := range reqs {
		switch req.Type {
		case "tcpip-forward":
			var p struct {
				Host string
				Port uint32
			}
			if ssh.Unmarshal(req.Payload, &p) != nil {
				reply(req, false)
				continue
			}
			port, ok := f.remoteTCP(p.Host, p.Port)
			if !ok {
				reply(req, false)
				continue
			}
			if req.WantReply {
				// A request for port 0 is told which port it got.
				_ = req.Reply(true, ssh.Marshal(struct{ Port uint32 }{port}))
			}
		case "cancel-tcpip-forward":
			var p struct {
				Host string
				Port uint32
			}
			ok := ssh.Unmarshal(req.Payload, &p) == nil && f.cancel(net.JoinHostPort(p.Host, strconv.FormatUint(uint64(p.Port), 10)))
			reply(req, ok)
		case "streamlocal-forward@openssh.com":
			var p struct{ Path string }
			reply(req, ssh.Unmarshal(req.Payload, &p) == nil && f.remoteUnix(p.Path))
		case "cancel-streamlocal-forward@openssh.com":
			var p struct{ Path string }
			reply(req, ssh.Unmarshal(req.Payload, &p) == nil && f.cancel(p.Path))
		default:
			reply(req, false)
		}
	}
}

// remoteTCP listens inside the container for a -R and returns the port it
// got. Each connection goes back to the client on a forwarded-tcpip channel
// naming the address and port it asked for, which is how it finds its -R.
func (f *forwards) remoteTCP(host string, port uint32) (uint32, bool) {
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.FormatUint(uint64(port), 10)))
	if err != nil {
		f.log.Warn("remote forward", "err", err)
		return 0, false
	}
	bound := uint32(ln.Addr().(*net.TCPAddr).Port)
	key := net.JoinHostPort(host, strconv.FormatUint(uint64(bound), 10))
	if !f.add(key, ln) {
		return 0, false
	}
	go f.accept(ln, func(c net.Conn) (string, []byte) {
		orig, origPort := splitAddr(c.RemoteAddr())
		return "forwarded-tcpip", ssh.Marshal(struct {
			Host     string
			Port     uint32
			OrigHost string
			OrigPort uint32
		}{host, bound, orig, origPort})
	})
	return bound, true
}

// remoteUnix listens on a Unix socket inside the container for a -R. A file
// already at that path is replaced, as DevPod does, so a forward survives a
// session that ended without cleaning up.
func (f *forwards) remoteUnix(path string) bool {
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		f.log.Warn("remote unix forward", "err", err)
		return false
	}
	if !f.add(path, ln) {
		return false
	}
	go f.accept(ln, func(net.Conn) (string, []byte) {
		return "forwarded-streamlocal@openssh.com", ssh.Marshal(struct {
			Path     string
			Reserved string
		}{path, ""})
	})
	return true
}

// accept sends every connection to ln back to the client on a channel of the
// type and payload open describes, until ln is closed.
func (f *forwards) accept(ln net.Listener, open func(net.Conn) (string, []byte)) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			kind, payload := open(c)
			ch, reqs, err := f.conn.OpenChannel(kind, payload)
			if err != nil {
				_ = c.Close()
				return
			}
			go ssh.DiscardRequests(reqs)
			join(ch, c)
		}()
	}
}

// add records a listener; it refuses once the connection has closed, so a
// request racing the end of the connection leaves nothing listening.
func (f *forwards) add(key string, ln net.Listener) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listeners == nil {
		_ = ln.Close()
		return false
	}
	if old, ok := f.listeners[key]; ok {
		_ = old.Close()
	}
	f.listeners[key] = ln
	return true
}

func (f *forwards) cancel(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	ln, ok := f.listeners[key]
	if ok {
		_ = ln.Close()
		delete(f.listeners, key)
	}
	return ok
}

// closeAll closes every remote listener; the connection is gone.
func (f *forwards) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ln := range f.listeners {
		_ = ln.Close()
	}
	f.listeners = nil
}

// join copies both ways between an SSH channel and a connection, passing on
// each side's end of input, and closes both when the two directions are done.
func join(ch ssh.Channel, c net.Conn) {
	defer ch.Close()
	defer c.Close()
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(ch, c)
		_ = ch.CloseWrite()
		close(done)
	}()
	_, _ = io.Copy(c, ch)
	if hc, ok := c.(interface{ CloseWrite() error }); ok {
		_ = hc.CloseWrite()
	}
	<-done
}

func splitAddr(a net.Addr) (string, uint32) {
	if t, ok := a.(*net.TCPAddr); ok {
		return t.IP.String(), uint32(t.Port)
	}
	return "", 0
}
