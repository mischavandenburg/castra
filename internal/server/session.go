package server

import (
	"bufio"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// session is one SSH session channel: a shell, a command, or the sftp
// subsystem, plus the agent socket when the client forwards its agent.
type session struct {
	conn    *ssh.ServerConn
	ch      ssh.Channel
	workdir string
	log     *slog.Logger
	gone    <-chan struct{} // closed when the client connection ends

	mu      sync.Mutex
	env     []string
	ptyReq  *ptyRequest
	ptmx    *os.File
	proc    *os.Process
	started bool

	agentDir string
	agentLn  net.Listener
}

type ptyRequest struct {
	Term   string
	Cols   uint32
	Rows   uint32
	Width  uint32
	Height uint32
	Modes  string
}

type windowChange struct {
	Cols   uint32
	Rows   uint32
	Width  uint32
	Height uint32
}

func (s *session) serve(reqs <-chan *ssh.Request) {
	defer s.cleanup()
	for req := range reqs {
		switch req.Type {
		case "env":
			var kv struct{ Name, Value string }
			if err := ssh.Unmarshal(req.Payload, &kv); err != nil {
				reply(req, false)
				continue
			}
			s.mu.Lock()
			s.env = append(s.env, kv.Name+"="+kv.Value)
			s.mu.Unlock()
			reply(req, true)

		case "pty-req":
			var p ptyRequest
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				reply(req, false)
				continue
			}
			s.mu.Lock()
			s.ptyReq = &p
			s.mu.Unlock()
			reply(req, true)

		case "window-change":
			var w windowChange
			if err := ssh.Unmarshal(req.Payload, &w); err == nil {
				s.resize(w.Cols, w.Rows)
			}
			reply(req, true)

		case "auth-agent-req@openssh.com":
			if err := s.forwardAgent(); err != nil {
				s.log.Warn("agent forwarding", "err", err)
				reply(req, false)
				continue
			}
			reply(req, true)

		case "shell", "exec":
			command := ""
			if req.Type == "exec" {
				var p struct{ Command string }
				if err := ssh.Unmarshal(req.Payload, &p); err != nil {
					reply(req, false)
					continue
				}
				command = p.Command
			}
			if !s.start() {
				reply(req, false)
				continue
			}
			reply(req, true)
			go s.run(command, req.Type == "shell")

		case "subsystem":
			var p struct{ Name string }
			if err := ssh.Unmarshal(req.Payload, &p); err != nil || p.Name != "sftp" || !s.start() {
				reply(req, false)
				continue
			}
			reply(req, true)
			go s.runSFTP()

		case "signal":
			var p struct{ Signal string }
			if err := ssh.Unmarshal(req.Payload, &p); err == nil {
				s.signal(p.Signal)
			}
			reply(req, true)

		default:
			reply(req, false)
		}
	}
}

func reply(req *ssh.Request, ok bool) {
	if req.WantReply {
		_ = req.Reply(ok, nil)
	}
}

// start marks the session as running its one program; a second shell or exec
// on the same channel is refused, as the protocol requires.
func (s *session) start() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return false
	}
	s.started = true
	return true
}

func (s *session) run(command string, interactive bool) {
	account := currentAccount()
	var cmd *exec.Cmd
	if interactive {
		cmd = exec.Command(account.shell)
		cmd.Args[0] = "-" + filepath.Base(account.shell) // login shell
	} else {
		cmd = exec.Command(account.shell, "-c", command)
	}
	cmd.Dir = s.startDir(account.home)

	s.mu.Lock()
	withPTY := s.ptyReq != nil
	cmd.Env = s.environ(account, s.ptyReq)
	s.mu.Unlock()

	var err error
	if withPTY {
		err = s.runPTY(cmd)
	} else {
		err = s.runPlain(cmd)
	}
	s.exit(err)
}

func (s *session) runPTY(cmd *exec.Cmd) error {
	// Size and start under the lock resize takes, so a window-change that
	// races the start is either used for it or applied to the open terminal.
	s.mu.Lock()
	ws := &pty.Winsize{Rows: clamp16(s.ptyReq.Rows), Cols: clamp16(s.ptyReq.Cols)}
	ptmx, err := pty.StartWithSize(cmd, ws)
	if err == nil {
		s.ptmx = ptmx
		s.proc = cmd.Process
	}
	s.mu.Unlock()
	if err != nil {
		return err
	}

	go func() { _, _ = io.Copy(ptmx, s.ch) }()
	output := make(chan struct{})
	go func() {
		_, _ = io.Copy(s.ch, ptmx) // ends with EIO once the process side closes
		close(output)
	}()
	exited := make(chan struct{})
	go s.hangUpWhenGone(cmd.Process, exited)

	err = cmd.Wait()
	close(exited)
	select {
	case <-output:
	case <-time.After(2 * time.Second): // a background job still holds the terminal
	}
	s.mu.Lock()
	s.ptmx = nil
	s.mu.Unlock()
	_ = ptmx.Close()
	return err
}

func (s *session) runPlain(cmd *exec.Cmd) error {
	cmd.Stdout = s.ch
	cmd.Stderr = s.ch.Stderr()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	s.mu.Lock()
	s.proc = cmd.Process
	s.mu.Unlock()
	go func() {
		_, _ = io.Copy(stdin, s.ch)
		_ = stdin.Close()
	}()
	exited := make(chan struct{})
	go s.hangUpWhenGone(cmd.Process, exited)
	err = cmd.Wait()
	close(exited)
	return err
}

// hangUpWhenGone sends SIGHUP to the session's process group when the client
// connection drops, the way a terminal hangup would. It stops watching once
// the process has exited, so a reused process group id is never signalled.
func (s *session) hangUpWhenGone(p *os.Process, exited <-chan struct{}) {
	select {
	case <-s.gone:
		_ = syscall.Kill(-p.Pid, syscall.SIGHUP)
	case <-exited:
	}
}

func (s *session) runSFTP() {
	var opts []sftp.ServerOption
	if dir := s.startDir(currentAccount().home); dir != "" {
		opts = append(opts, sftp.WithServerWorkingDirectory(dir))
	}
	srv, err := sftp.NewServer(s.ch, opts...)
	if err != nil {
		s.exit(err)
		return
	}
	// Not srv.Close: it closes the channel, and the exit status must go out
	// first or OpenSSH's scp reports failure. exit closes the channel after.
	err = srv.Serve()
	if errors.Is(err, io.EOF) {
		err = nil
	}
	s.exit(err)
}

// exit reports how the program ended and closes the channel.
func (s *session) exit(err error) {
	var exitErr *exec.ExitError
	switch {
	case err == nil:
		s.sendExitStatus(0)
	case errors.As(err, &exitErr):
		ws, ok := exitErr.Sys().(syscall.WaitStatus)
		if ok && ws.Signaled() {
			msg := struct {
				Signal     string
				CoreDumped bool
				Error      string
				Lang       string
			}{Signal: signalName(ws.Signal()), CoreDumped: ws.CoreDump()}
			_, _ = s.ch.SendRequest("exit-signal", false, ssh.Marshal(&msg))
		} else {
			s.sendExitStatus(uint32(exitErr.ExitCode()))
		}
	default:
		_, _ = io.WriteString(s.ch.Stderr(), "castra: "+err.Error()+"\n")
		s.sendExitStatus(127)
	}
	_ = s.ch.Close()
}

func (s *session) sendExitStatus(code uint32) {
	_, _ = s.ch.SendRequest("exit-status", false, ssh.Marshal(&struct{ Status uint32 }{code}))
}

func (s *session) resize(cols, rows uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ptyReq != nil {
		s.ptyReq.Cols, s.ptyReq.Rows = cols, rows
	}
	if s.ptmx != nil {
		_ = pty.Setsize(s.ptmx, &pty.Winsize{Rows: clamp16(rows), Cols: clamp16(cols)})
	}
}

func (s *session) signal(name string) {
	s.mu.Lock()
	p := s.proc
	s.mu.Unlock()
	if p == nil {
		return
	}
	if sig, ok := signals[name]; ok {
		_ = p.Signal(sig)
	}
}

// forwardAgent creates a socket only this session can see and relays every
// connection to it back to the client's agent over an auth-agent channel.
func (s *session) forwardAgent() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agentLn != nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "castra-agent-")
	if err != nil {
		return err
	}
	sock := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		_ = os.RemoveAll(dir)
		return err
	}
	_ = os.Chmod(sock, 0o600)
	s.agentDir, s.agentLn = dir, ln

	go func() {
		for {
			local, err := ln.Accept()
			if err != nil {
				return
			}
			go s.relayAgent(local)
		}
	}()
	return nil
}

func (s *session) relayAgent(local net.Conn) {
	defer local.Close()
	remote, reqs, err := s.conn.OpenChannel("auth-agent@openssh.com", nil)
	if err != nil {
		s.log.Warn("open agent channel", "err", err)
		return
	}
	defer remote.Close()
	go ssh.DiscardRequests(reqs)
	go func() {
		_, _ = io.Copy(remote, local)
		_ = remote.CloseWrite()
	}()
	_, _ = io.Copy(local, remote)
}

func (s *session) agentSocket() string {
	if s.agentLn == nil {
		return ""
	}
	return s.agentLn.Addr().String()
}

func (s *session) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agentLn != nil {
		_ = s.agentLn.Close()
		_ = os.RemoveAll(s.agentDir)
		s.agentLn = nil
	}
}

func (s *session) startDir(home string) string {
	if s.workdir != "" {
		if fi, err := os.Stat(s.workdir); err == nil && fi.IsDir() {
			return s.workdir
		}
	}
	return home
}

// environ is the container's environment (what `podman exec` handed us) plus
// the login variables, the client's env requests, the terminal and the agent.
// Called with s.mu held.
func (s *session) environ(a account, req *ptyRequest) []string {
	vars := map[string]string{}
	var order []string
	set := func(kv string) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return
		}
		if _, seen := vars[k]; !seen {
			order = append(order, k)
		}
		vars[k] = v
	}
	for _, kv := range os.Environ() {
		set(kv)
	}
	for _, kv := range s.env {
		set(kv)
	}
	set("HOME=" + a.home)
	set("USER=" + a.name)
	set("LOGNAME=" + a.name)
	set("SHELL=" + a.shell)
	if req != nil && req.Term != "" {
		set("TERM=" + req.Term)
	}
	if sock := s.agentSocket(); sock != "" {
		set("SSH_AUTH_SOCK=" + sock)
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+vars[k])
	}
	return out
}

type account struct {
	name, home, shell string
}

// currentAccount reads the user castra runs as from /etc/passwd, since the
// login shell is not exposed by os/user.
func currentAccount() account {
	a := account{name: "user", home: "/", shell: "/bin/sh"}
	if u, err := user.Current(); err == nil {
		a.name, a.home = u.Username, u.HomeDir
	}
	uid := strconv.Itoa(os.Getuid())
	if f, err := os.Open("/etc/passwd"); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Split(sc.Text(), ":")
			if len(fields) >= 7 && fields[2] == uid {
				if fields[0] != "" {
					a.name = fields[0]
				}
				if fields[5] != "" {
					a.home = fields[5]
				}
				if fields[6] != "" {
					a.shell = fields[6]
				}
				break
			}
		}
	}
	if _, err := os.Stat(a.shell); err != nil {
		a.shell = "/bin/sh"
	}
	return a
}

func clamp16(v uint32) uint16 {
	if v > 0xffff {
		return 0xffff
	}
	return uint16(v)
}

var signals = map[string]syscall.Signal{
	"ABRT": syscall.SIGABRT, "ALRM": syscall.SIGALRM, "FPE": syscall.SIGFPE,
	"HUP": syscall.SIGHUP, "ILL": syscall.SIGILL, "INT": syscall.SIGINT,
	"KILL": syscall.SIGKILL, "PIPE": syscall.SIGPIPE, "QUIT": syscall.SIGQUIT,
	"SEGV": syscall.SIGSEGV, "TERM": syscall.SIGTERM, "USR1": syscall.SIGUSR1,
	"USR2": syscall.SIGUSR2,
}

func signalName(sig syscall.Signal) string {
	for name, s := range signals {
		if s == sig {
			return name
		}
	}
	return "TERM"
}
