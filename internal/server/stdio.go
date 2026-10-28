package server

import (
	"io"
	"net"
	"time"
)

// stdioConn presents a reader and a writer (the server's stdin and stdout,
// piped through `podman exec -i`) as the net.Conn the SSH library expects.
type stdioConn struct {
	r io.ReadCloser
	w io.WriteCloser
}

// NewStdioConn wraps a read and a write stream as a net.Conn.
func NewStdioConn(r io.ReadCloser, w io.WriteCloser) net.Conn {
	return &stdioConn{r: r, w: w}
}

func (c *stdioConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *stdioConn) Write(p []byte) (int, error) { return c.w.Write(p) }

func (c *stdioConn) Close() error {
	werr := c.w.Close()
	rerr := c.r.Close()
	if werr != nil {
		return werr
	}
	return rerr
}

type stdioAddr struct{}

func (stdioAddr) Network() string { return "stdio" }
func (stdioAddr) String() string  { return "stdio" }

func (c *stdioConn) LocalAddr() net.Addr                { return stdioAddr{} }
func (c *stdioConn) RemoteAddr() net.Addr               { return stdioAddr{} }
func (c *stdioConn) SetDeadline(t time.Time) error      { return nil }
func (c *stdioConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *stdioConn) SetWriteDeadline(t time.Time) error { return nil }
