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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

type harness struct {
	client  *ssh.Client
	workdir string
}

// start runs the server on one loopback connection (net.Pipe deadlocks the
// SSH version exchange, which both sides write at once) and dials it with
// the x/crypto client the way OpenSSH reaches castra: no key, and the host
// key not checked.
func start(t *testing.T) (*harness, error) {
	t.Helper()
	return startWith(t, nil)
}

func startWith(t *testing.T, remoteEnv map[string]*string) (*harness, error) {
	t.Helper()
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
		_ = Serve(c, Options{Workdir: workdir, Workspace: "demo", RemoteEnv: remoteEnv})
	}()

	client, err := ssh.Dial("tcp", ln.Addr().String(), &ssh.ClientConfig{
		User:            "test",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { client.Close() })
	return &harness{client: client, workdir: workdir}, nil
}

// TestAnyClientGetsIn: like DevPod's server, castra's asks for no key; the
// engine's exec is the access control.
func TestAnyClientGetsIn(t *testing.T) {
	h, err := start(t)
	if err != nil {
		t.Fatalf("a client with no key was refused: %v", err)
	}
	s, _ := h.client.NewSession()
	if out, err := s.Output("echo in"); err != nil || string(out) != "in\n" {
		t.Fatalf("got %q, %v", out, err)
	}
}

func TestExecOutputAndExitStatus(t *testing.T) {
	h, err := start(t)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := h.client.NewSession()
	out, err := s.Output("pwd; echo hi; exit 3")
	var exitErr *ssh.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitStatus() != 3 {
		t.Fatalf("want exit status 3, got %v", err)
	}
	workdir, _ := filepath.EvalSymlinks(h.workdir) // macOS temp dirs sit behind /private
	if want := workdir + "\nhi\n"; string(out) != want {
		t.Fatalf("output %q, want %q", out, want)
	}
}

// TestDevcontainerMarkers: dotfiles test REMOTE_CONTAINERS to know they are in
// a devcontainer; without it a common setup replaces the forwarded agent.
// Clear all three because inside a castra container they are already set, and
// those inherited values would win over the test server's markers.
func TestDevcontainerMarkers(t *testing.T) {
	for _, k := range []string{"REMOTE_CONTAINERS", "CASTRA", "CASTRA_WORKSPACE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	h, err := start(t)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := h.client.NewSession()
	out, err := s.Output(`printf '%s %s %s' "$REMOTE_CONTAINERS" "$CASTRA" "$CASTRA_WORKSPACE"`)
	if err != nil || string(out) != "true true demo" {
		t.Fatalf("markers %q, %v", out, err)
	}
}

func TestRemoteEnvAppliesAndUnsets(t *testing.T) {
	t.Setenv("CASTRA_TEST_UNSET_ME", "from-the-container")
	t.Setenv("CASTRA_TEST_OVERRIDE", "from-the-container")
	val := func(s string) *string { return &s }
	h, err := startWith(t, map[string]*string{
		"CASTRA_TEST_REMOTE":   val("from-remoteEnv"),
		"CASTRA_TEST_OVERRIDE": val("from-remoteEnv"),
		"CASTRA_TEST_UNSET_ME": nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := h.client.NewSession()
	out, err := s.Output(`printf '%s|%s|%s' "$CASTRA_TEST_REMOTE" "$CASTRA_TEST_OVERRIDE" "${CASTRA_TEST_UNSET_ME-unset}"`)
	if err != nil || string(out) != "from-remoteEnv|from-remoteEnv|unset" {
		t.Fatalf("remoteEnv applied as %q, %v", out, err)
	}
}

func TestContainerEnvWinsOverMarkers(t *testing.T) {
	t.Setenv("REMOTE_CONTAINERS", "set-by-the-container")
	h, err := start(t)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := h.client.NewSession()
	out, err := s.Output(`printf %s "$REMOTE_CONTAINERS"`)
	if err != nil || string(out) != "set-by-the-container" {
		t.Fatalf("REMOTE_CONTAINERS %q, %v", out, err)
	}
}

func TestStdinReachesCommand(t *testing.T) {
	h, err := start(t)
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

func TestPTY(t *testing.T) {
	h, err := start(t)
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
	isTTY := strings.HasPrefix(got, "/dev/pts/") || strings.HasPrefix(got, "/dev/ttys") // Linux, macOS
	if !isTTY || !strings.Contains(got, "40 120\n") || !strings.Contains(got, "xterm-256color") {
		t.Fatalf("pty session printed %q", got)
	}
}

func TestWindowChangeReachesTerminal(t *testing.T) {
	h, err := start(t)
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
	h, err := start(t)
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

// TestAgentConnectionsCloseWithSession: a process that connected to the
// forwarded socket while the session ran loses the agent when the session
// ends, even if it never lets go of its connection.
func TestAgentConnectionsCloseWithSession(t *testing.T) {
	h, err := start(t)
	if err != nil {
		t.Fatal(err)
	}
	keyring := agent.NewKeyring()
	_, agentKey, _ := ed25519.GenerateKey(rand.Reader)
	_ = keyring.Add(agent.AddedKey{PrivateKey: agentKey, Comment: "held-key"})
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
	sock, _ := bufio.NewReader(stdout).ReadString('\n')
	held, err := net.Dial("unix", strings.TrimSpace(sock))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if keys, err := agent.NewClient(held).List(); err != nil || len(keys) != 1 {
		t.Fatalf("before the session ended: %v, %v", keys, err)
	}

	_, _ = io.WriteString(stdin, "done\n")
	if err := s.Wait(); err != nil {
		t.Fatal(err)
	}
	_ = held.SetDeadline(time.Now().Add(3 * time.Second))
	if keys, err := agent.NewClient(held).List(); err == nil {
		t.Fatalf("the held connection still lists %d key(s) after the session ended", len(keys))
	}
}

// TestHangUpOnDisconnect: a program whose client goes away gets SIGHUP from
// the server, the way a terminal hangup would send it.
func TestHangUpOnDisconnect(t *testing.T) {
	h, err := start(t)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := h.client.NewSession()
	stdout, _ := s.StdoutPipe()
	if err := s.Start(`echo $$; exec sleep 30`); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("pid line %q", line)
	}
	// Disconnect only once the shell has become sleep. A shell such as zsh
	// catches SIGHUP itself, and one that lands between `echo` and `exec` is
	// lost with the shell; that race is the shell's, not castra's.
	waitFor(t, func() bool {
		comm, _ := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
		return strings.HasSuffix(strings.TrimSpace(string(comm)), "sleep")
	}, "pid %d to become sleep", pid)
	_ = h.client.Close() // the client is gone, mid-command
	waitFor(t, func() bool { return syscall.Kill(pid, 0) != nil }, "sleep %d to end after its client disconnected", pid)
}

// waitFor polls cond for up to five seconds; on timeout it fails the test
// with the description.
func waitFor(t *testing.T, cond func() bool, what string, args ...any) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for "+what, args...)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSessionWithoutAgentHasNoSocket(t *testing.T) {
	h, err := start(t)
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
	h, err := start(t)
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
