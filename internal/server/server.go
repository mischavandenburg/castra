// Package server is the SSH server castra runs inside the container. It speaks
// SSH on a single connection (stdin and stdout of `podman exec -i`), forwards
// the client's ssh-agent for the length of a session, and carries port
// forwards both ways.
//
// Like DevPod's server, it asks the client for no key and makes its host key
// fresh for every run. The access control is the engine's `exec`, which only
// the owner of the container can run; there is no network in between.
package server

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Options configures one server run.
type Options struct {
	// Workdir is where sessions start. Falls back to the user's home.
	Workdir string
	// Workspace is the workspace name, exported to sessions as CASTRA_WORKSPACE.
	Workspace string
	// RemoteEnv is devcontainer.json's resolved remoteEnv, applied on top of
	// the container's environment; a nil value unsets the variable.
	RemoteEnv map[string]*string
	// Log receives diagnostics. Never stdout: stdout is the SSH stream.
	Log *slog.Logger
}

// Serve runs the SSH protocol on conn until the client disconnects and every
// session has ended. Remote forwards the client asked for close with it.
func Serve(conn net.Conn, opts Options) error {
	cfg, err := serverConfig()
	if err != nil {
		return err
	}
	log := opts.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return fmt.Errorf("ssh handshake: %w", err)
	}
	defer sconn.Close()
	fwd := newForwards(sconn, log)
	defer fwd.closeAll()
	go fwd.serve(reqs)

	var wg sync.WaitGroup
	for nc := range chans {
		switch nc.ChannelType() {
		case "session":
			ch, chReqs, err := nc.Accept()
			if err != nil {
				log.Warn("accept channel", "err", err)
				continue
			}
			s := &session{conn: sconn, ch: ch, workdir: opts.Workdir, workspace: opts.Workspace, remoteEnv: opts.RemoteEnv, log: log}
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.serve(chReqs)
			}()
		case "direct-tcpip":
			go fwd.local(nc, "tcp")
		case "direct-streamlocal@openssh.com":
			go fwd.local(nc, "unix")
		default:
			_ = nc.Reject(ssh.UnknownChannelType, "castra does not serve "+nc.ChannelType()+" channels")
		}
	}
	wg.Wait()
	return nil
}

// serverConfig lets any client in, as DevPod's server does when started
// without authorized keys, with an ed25519 host key made for this run.
func serverConfig() (*ssh.ServerConfig, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ServerConfig{ServerVersion: "SSH-2.0-castra", NoClientAuth: true}
	cfg.AddHostKey(signer)
	return cfg, nil
}

// ServeStdio runs Serve on the process's own stdin and stdout.
func ServeStdio(opts Options) error {
	return Serve(NewStdioConn(os.Stdin, os.Stdout), opts)
}
