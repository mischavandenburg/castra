package server

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/mischavandenburg/castra/internal/sshconfig"
)

type harness struct {
	client    *ssh.Client
	clientKey ssh.Signer
	workdir   string
}

// start runs the server on one loopback connection (net.Pipe deadlocks the
// SSH version exchange, which both sides write at once) and dials it with
// the x/crypto client.
func start(t *testing.T, dialKey ssh.Signer) (*harness, error) {
	t.Helper()
	hostPEM, hostPub, err := sshconfig.NewKey("test-host")
	if err != nil {
		t.Fatal(err)
	}
	_, clientPriv, _ := ed25519.GenerateKey(rand.Reader)
	clientSigner, _ := ssh.NewSignerFromKey(clientPriv)
	if dialKey == nil {
		dialKey = clientSigner
	}
	workdir := t.TempDir()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_ = Serve(c, Options{
			HostKey:       hostPEM,
			AuthorizedKey: ssh.MarshalAuthorizedKey(clientSigner.PublicKey()),
			Workdir:       workdir,
		})
	}()

	client, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{
		User:            "test",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(dialKey)},
		HostKeyCallback: ssh.FixedHostKey(hostPub),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { client.Close() })
	return &harness{client: client, clientKey: clientSigner, workdir: workdir}, nil
}

func TestExecOutputAndExitStatus(t *testing.T) {
	h, err := start(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := h.client.NewSession()
	out, err := s.Output("pwd; echo hi; exit 3")
	var exitErr *ssh.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitStatus() != 3 {
		t.Fatalf("want exit status 3, got %v", err)
	}
	if want := h.workdir + "\nhi\n"; string(out) != want {
		t.Fatalf("output %q, want %q", out, want)
	}
}

func TestStdinReachesCommand(t *testing.T) {
	h, err := start(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := h.client.NewSession()
	s.Stdin = strings.NewReader("from stdin\n")
	out, err := s.Output("cat")
	if err != nil || string(out) != "from stdin\n" {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestRejectsUnknownKey(t *testing.T) {
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(other)
	if _, err := start(t, otherSigner); err == nil {
		t.Fatal("a key that is not the authorized one got in")
	}
}

func TestPTY(t *testing.T) {
	h, err := start(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := h.client.NewSession()
	if err := s.RequestPty("xterm-256color", 40, 120, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	out, err := s.Output("tty; stty size; echo $TERM")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.ReplaceAll(string(out), "\r", "")
	if !strings.HasPrefix(got, "/dev/pts/") || !strings.Contains(got, "40 120\n") || !strings.Contains(got, "xterm-256color") {
		t.Fatalf("pty session printed %q", got)
	}
}

func TestWindowChangeReachesTerminal(t *testing.T) {
	h, err := start(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := h.client.NewSession()
	if err := s.RequestPty("xterm", 24, 80, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	s.Stdout = &out
	if err := s.Start("sleep 1; stty size"); err != nil {
		t.Fatal(err)
	}
	if err := s.WindowChange(50, 132); err != nil {
		t.Fatal(err)
	}
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(strings.ReplaceAll(out.String(), "\r", "")); got != "50 132" {
		t.Fatalf("stty size after the resize: %q", got)
	}
}

// TestAgentForwarding lists a key through the forwarded socket while the
// session is alive, then checks the socket is gone once the session ends.
func TestAgentForwarding(t *testing.T) {
	h, err := start(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	keyring := agent.NewKeyring()
	_, agentKey, _ := ed25519.GenerateKey(rand.Reader)
	if err := keyring.Add(agent.AddedKey{PrivateKey: agentKey, Comment: "forwarded-test-key"}); err != nil {
		t.Fatal(err)
	}
	if err := agent.ForwardToAgent(h.client, keyring); err != nil {
		t.Fatal(err)
	}
	s, _ := h.client.NewSession()
	if err := agent.RequestAgentForwarding(s); err != nil {
		t.Fatal(err)
	}
	stdout, _ := s.StdoutPipe()
	stdin, _ := s.StdinPipe()
	if err := s.Start(`echo "$SSH_AUTH_SOCK"; read _`); err != nil {
		t.Fatal(err)
	}
	sock, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	sock = strings.TrimSpace(sock)
	if !strings.HasPrefix(sock, os.TempDir()) {
		t.Fatalf("SSH_AUTH_SOCK %q is not under %s", sock, os.TempDir())
	}

	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := agent.NewClient(c).List()
	c.Close()
	if err != nil || len(keys) != 1 || keys[0].Comment != "forwarded-test-key" {
		t.Fatalf("listed %v through the forwarded socket, err %v", keys, err)
	}

	_, _ = io.WriteString(stdin, "done\n")
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(sock)
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent socket directory %s still exists after the session ended", dir)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestSessionWithoutAgentHasNoSocket(t *testing.T) {
	h, err := start(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := h.client.NewSession()
	t.Setenv("SSH_AUTH_SOCK", "")
	out, err := s.Output(`printf %s "$SSH_AUTH_SOCK"`)
	if err != nil || len(out) != 0 {
		t.Fatalf("SSH_AUTH_SOCK without forwarding: %q, %v", out, err)
	}
}

// TestSFTP copies a file over the sftp subsystem and requires the exit status
// OpenSSH's scp waits for; without it scp exits 1 even though the copy landed.
func TestSFTP(t *testing.T) {
	h, err := start(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	ch, reqs, err := h.client.OpenChannel("session", nil)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := ch.SendRequest("subsystem", true, ssh.Marshal(&struct{ Name string }{"sftp"})); err != nil || !ok {
		t.Fatalf("subsystem request: ok=%v err=%v", ok, err)
	}
	c, err := sftp.NewClientPipe(ch, halfCloser{ch})
	if err != nil {
		t.Fatal(err)
	}
	f, err := c.Create("copied.txt") // relative: lands in the workdir
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Write([]byte("over sftp"))
	f.Close()
	c.Close() // sends EOF, as scp does, and waits for the server's answer

	status, ok := exitStatus(reqs)
	if !ok || status != 0 {
		t.Fatalf("sftp channel closed with exit status %d (sent: %v), want 0", status, ok)
	}
	got, err := os.ReadFile(filepath.Join(h.workdir, "copied.txt"))
	if err != nil || !bytes.Equal(got, []byte("over sftp")) {
		t.Fatalf("read back %q, %v", got, err)
	}
}

// halfCloser turns Close into an SSH EOF, leaving the channel open for the
// server's exit status.
type halfCloser struct{ ssh.Channel }

func (h halfCloser) Close() error { return h.CloseWrite() }

// exitStatus waits for the channel's exit-status request.
func exitStatus(reqs <-chan *ssh.Request) (uint32, bool) {
	timeout := time.After(5 * time.Second)
	for {
		select {
		case req, open := <-reqs:
			if !open {
				return 0, false
			}
			if req.Type == "exit-status" {
				var p struct{ Status uint32 }
				if ssh.Unmarshal(req.Payload, &p) == nil {
					return p.Status, true
				}
			}
		case <-timeout:
			return 0, false
		}
	}
}
