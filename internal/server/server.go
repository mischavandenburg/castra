// Package server is the SSH server castra runs inside the container. It speaks
// SSH on a single connection (stdin and stdout of `podman exec -i`), accepts one
// client key, and forwards the client's ssh-agent for the length of a session.
package server

import (
	"crypto/subtle"
	"errors"
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
	// HostKey is the PEM-encoded private host key the client has pinned.
	HostKey []byte
	// AuthorizedKey is the one client public key allowed in, in
	// authorized_keys format.
	AuthorizedKey []byte
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
// session has ended.
func Serve(conn net.Conn, opts Options) error {
	cfg, err := serverConfig(opts)
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
	go ssh.DiscardRequests(reqs)

	var wg sync.WaitGroup
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "castra serves session channels only")
			continue
		}
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
	}
	wg.Wait()
	return nil
}

func serverConfig(opts Options) (*ssh.ServerConfig, error) {
	signer, err := ssh.ParsePrivateKey(opts.HostKey)
	if err != nil {
		return nil, fmt.Errorf("parse host key: %w", err)
	}
	allowed, _, _, _, err := ssh.ParseAuthorizedKey(opts.AuthorizedKey)
	if err != nil {
		return nil, fmt.Errorf("parse authorized key: %w", err)
	}
	want := allowed.Marshal()
	cfg := &ssh.ServerConfig{
		ServerVersion: "SSH-2.0-castra",
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if subtle.ConstantTimeCompare(key.Marshal(), want) == 1 {
				return &ssh.Permissions{}, nil
			}
			return nil, errors.New("key not authorized")
		},
	}
	cfg.AddHostKey(signer)
	return cfg, nil
}

// ServeStdio runs Serve on the process's own stdin and stdout.
func ServeStdio(opts Options) error {
	return Serve(NewStdioConn(os.Stdin, os.Stdout), opts)
}
