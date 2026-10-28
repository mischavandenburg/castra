// Package sshconfig owns castra's files under ~/.ssh/castra: the client key,
// one Host block and one pinned host key per workspace, and the Include that
// makes OpenSSH read them. Everything per workspace is its own file, so two
// `castra up` on different folders never touch the same file.
package sshconfig

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/mischavandenburg/castra/internal/atomic"
	"github.com/mischavandenburg/castra/internal/lock"
	"github.com/mischavandenburg/castra/internal/shell"
	"github.com/mischavandenburg/castra/internal/workspace"
)

// Paths are the files castra manages for OpenSSH.
type Paths struct {
	SSHDir     string // ~/.ssh
	Dir        string // ~/.ssh/castra
	Config     string // ~/.ssh/castra/config: includes every host file
	HostsDir   string // ~/.ssh/castra/hosts: one <name>.conf per workspace
	PinsDir    string // ~/.ssh/castra/known_hosts.d: one <name> per workspace
	ClientKey  string // ~/.ssh/castra/id_ed25519
	UserConfig string // ~/.ssh/config
}

// DefaultPaths resolves the paths under the user's home.
func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	sshDir := filepath.Join(home, ".ssh")
	dir := filepath.Join(sshDir, "castra")
	return Paths{
		SSHDir:     sshDir,
		Dir:        dir,
		Config:     filepath.Join(dir, "config"),
		HostsDir:   filepath.Join(dir, "hosts"),
		PinsDir:    filepath.Join(dir, "known_hosts.d"),
		ClientKey:  filepath.Join(dir, "id_ed25519"),
		UserConfig: filepath.Join(sshDir, "config"),
	}, nil
}

// HostFile is the workspace's Host block.
func (p Paths) HostFile(name string) string { return filepath.Join(p.HostsDir, name+".conf") }

// PinFile holds the workspace's pinned host key, in known_hosts format.
func (p Paths) PinFile(name string) string { return filepath.Join(p.PinsDir, name) }

func (p Paths) ensureDirs() error {
	for _, d := range []string{p.SSHDir, p.Dir, p.HostsDir, p.PinsDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// locked runs fn while holding the lock on the files every workspace shares:
// the client key, castra's config and the Include in ~/.ssh/config.
func (p Paths) locked(fn func() error) error {
	if err := p.ensureDirs(); err != nil {
		return err
	}
	held, err := lock.Acquire(filepath.Join(p.Dir, ".lock"))
	if err != nil {
		return err
	}
	defer func() { _ = held.Release() }()
	return fn()
}

// EnsureClientKey creates castra's client key pair once and returns the
// public half in authorized_keys format.
func (p Paths) EnsureClientKey() (pub []byte, err error) {
	err = p.locked(func() error {
		pubPath := p.ClientKey + ".pub"
		if b, err := os.ReadFile(pubPath); err == nil {
			if _, err := os.Stat(p.ClientKey); err == nil {
				pub = b
				return nil
			}
		}
		privPEM, key, err := NewKey("castra")
		if err != nil {
			return err
		}
		if err := atomic.WriteFile(p.ClientKey, privPEM, 0o600); err != nil {
			return err
		}
		pub = ssh.MarshalAuthorizedKey(key)
		return atomic.WriteFile(pubPath, pub, 0o644)
	})
	return pub, err
}

// NewKey generates an ed25519 key pair: the private key as OpenSSH PEM, and
// the public key.
func NewKey(comment string) ([]byte, ssh.PublicKey, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, nil, err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(block), sshPub, nil
}

// PinHostKey records key as the only valid host key for the workspace,
// replacing any earlier one (a recreated container gets a new key).
func (p Paths) PinHostKey(s workspace.State, key ssh.PublicKey) error {
	if err := p.ensureDirs(); err != nil {
		return err
	}
	line := knownhosts.Line([]string{s.Alias()}, key)
	return atomic.WriteFile(p.PinFile(s.Name), []byte(line+"\n"), 0o600)
}

// HasHostKey reports whether a host key is pinned for the workspace.
func (p Paths) HasHostKey(s workspace.State) bool {
	fi, err := os.Stat(p.PinFile(s.Name))
	return err == nil && fi.Size() > 0
}

// WriteHost writes the workspace's Host block and makes sure castra's config
// includes the host files.
func (p Paths) WriteHost(s workspace.State, castraBin string) error {
	if err := p.ensureDirs(); err != nil {
		return err
	}
	if err := p.writeHost(s, castraBin); err != nil {
		return err
	}
	return p.ensureConfig()
}

func (p Paths) writeHost(s workspace.State, castraBin string) error {
	block, err := Render(s, castraBin, p)
	if err != nil {
		return err
	}
	return atomic.WriteFile(p.HostFile(s.Name), []byte(block), 0o600)
}

// RemoveHost removes the workspace's Host block and pinned host key.
func (p Paths) RemoveHost(s workspace.State) error {
	for _, f := range []string{p.HostFile(s.Name), p.PinFile(s.Name)} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Render produces the workspace's Host block. Every value is encoded for
// ssh_config here, at the one place that writes it, so a record from disk
// is as safe as a fresh one.
func Render(s workspace.State, castraBin string, p Paths) (string, error) {
	user, err := value(s.RemoteUser)
	if err != nil {
		return "", fmt.Errorf("remote user: %w", err)
	}
	proxy, err := ProxyCommand(castraBin, s.Name)
	if err != nil {
		return "", err
	}
	identity, err := pathValue(p.ClientKey)
	if err != nil {
		return "", err
	}
	pin, err := pathValue(p.PinFile(s.Name))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by castra for workspace %s. Rewritten on `castra up`; edits are lost.\n", s.Name)
	fmt.Fprintf(&b, "Host %s\n", s.Alias())
	fmt.Fprintf(&b, "  HostKeyAlias %s\n", s.Alias())
	fmt.Fprintf(&b, "  User %s\n", user)
	fmt.Fprintf(&b, "  ProxyCommand %s\n", proxy)
	fmt.Fprintf(&b, "  IdentityFile %s\n", identity)
	b.WriteString("  IdentitiesOnly yes\n")
	fmt.Fprintf(&b, "  UserKnownHostsFile %s\n", pin)
	b.WriteString("  StrictHostKeyChecking yes\n")
	b.WriteString("  UpdateHostKeys no\n")
	// Only castra's key, and never a prompt: a hostile container running a
	// server of its own cannot ask for a password.
	b.WriteString("  PreferredAuthentications publickey\n")
	b.WriteString("  PasswordAuthentication no\n")
	b.WriteString("  KbdInteractiveAuthentication no\n")
	// The agent is the one thing forwarded, whatever a `Host *` block says.
	b.WriteString("  ForwardAgent yes\n")
	b.WriteString("  ForwardX11 no\n")
	b.WriteString("  ClearAllForwardings yes\n")
	b.WriteString("  ControlMaster no\n")
	b.WriteString("  ControlPath none\n")
	b.WriteString("  ServerAliveInterval 30\n")
	b.WriteString("  LogLevel ERROR\n")
	return b.String(), nil
}

// ProxyCommand is the line OpenSSH runs to reach a workspace: castra itself,
// which execs `podman exec -i` into the container's server.
func ProxyCommand(castraBin, name string) (string, error) {
	if _, err := value(castraBin); err != nil {
		return "", fmt.Errorf("castra binary path: %w", err)
	}
	// OpenSSH expands %-tokens in ProxyCommand before handing it to the shell.
	bin := strings.ReplaceAll(shell.Quote(castraBin), "%", "%%")
	return bin + " proxy " + name, nil
}

// value encodes s as one ssh_config argument. The grammar has no escape for
// control characters or double quotes, so those are refused; whitespace is
// allowed inside double quotes.
func value(s string) (string, error) {
	if s == "" {
		return "", errors.New("empty value")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r == '"' {
			return "", fmt.Errorf("%q cannot be written to ssh_config", s)
		}
	}
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`, nil
	}
	return s, nil
}

// pathValue is value for keywords that expand %-tokens (IdentityFile,
// UserKnownHostsFile, Include).
func pathValue(s string) (string, error) {
	return value(strings.ReplaceAll(s, "%", "%%"))
}

// ensureConfig writes castra's config, one Include of every host file, when
// it is missing or says something else. Migrate calls it with the lock held,
// so it takes none itself; the write is atomic and the content is constant.
func (p Paths) ensureConfig() error {
	glob, err := pathValue(filepath.Join(p.HostsDir, "*.conf"))
	if err != nil {
		return err
	}
	want := "# Managed by castra. One file per workspace lives in hosts/.\nInclude " + glob + "\n"
	if b, err := os.ReadFile(p.Config); err == nil && string(b) == want {
		return nil
	}
	return atomic.WriteFile(p.Config, []byte(want), 0o600)
}

const (
	includeBegin = "# BEGIN castra"
	includeEnd   = "# END castra"
)

// HasInclude reports whether ~/.ssh/config already includes castra's config.
func (p Paths) HasInclude() bool {
	b, err := os.ReadFile(p.UserConfig)
	return err == nil && strings.Contains(string(b), includeBegin)
}

// EnsureInclude adds one Include line for castra's config to the top of
// ~/.ssh/config, between markers, if it is not there yet. It reports whether
// the file changed.
func (p Paths) EnsureInclude() (changed bool, err error) {
	err = p.locked(func() error {
		if p.HasInclude() {
			return nil
		}
		old, err := os.ReadFile(p.UserConfig)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		include, err := pathValue(p.Config)
		if err != nil {
			return err
		}
		block := fmt.Sprintf("%s\nInclude %s\n%s\n\n", includeBegin, include, includeEnd)
		mode := os.FileMode(0o600)
		if fi, err := os.Stat(p.UserConfig); err == nil {
			mode = fi.Mode().Perm()
		}
		changed = true
		return atomic.WriteFile(p.UserConfig, append([]byte(block), old...), mode)
	})
	return changed, err
}

// Migrate moves the layout castra used before 2026-10-28 (one shared config
// and one shared known_hosts for every workspace) to one file per workspace.
// It does nothing once the shared known_hosts is gone, which costs one stat.
func (p Paths) Migrate(load func() ([]workspace.State, error), castraBin string) error {
	legacy := filepath.Join(p.Dir, "known_hosts")
	if _, err := os.Stat(legacy); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return p.locked(func() error {
		b, err := os.ReadFile(legacy)
		if errors.Is(err, os.ErrNotExist) {
			return nil // another run migrated while we waited for the lock
		}
		if err != nil {
			return err
		}
		states, err := load()
		if err != nil {
			return err
		}
		for _, s := range states {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, s.Alias()+" ") && !p.HasHostKey(s) {
					if err := atomic.WriteFile(p.PinFile(s.Name), []byte(line+"\n"), 0o600); err != nil {
						return err
					}
				}
			}
			if _, err := os.Stat(p.HostFile(s.Name)); errors.Is(err, os.ErrNotExist) {
				if err := p.writeHost(s, castraBin); err != nil {
					return err
				}
			}
		}
		if err := p.ensureConfig(); err != nil {
			return err
		}
		return os.Remove(legacy)
	})
}
