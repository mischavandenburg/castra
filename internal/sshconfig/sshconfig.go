// Package sshconfig owns castra's files under ~/.ssh/castra: the client key,
// the pinned host keys, and the config block per workspace that OpenSSH reads.
package sshconfig

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"al.essio.dev/pkg/shellescape"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/mischavandenburg/castra/internal/workspace"
)

// Paths are the files castra manages for OpenSSH.
type Paths struct {
	SSHDir     string // ~/.ssh
	Dir        string // ~/.ssh/castra
	Config     string // ~/.ssh/castra/config
	KnownHosts string // ~/.ssh/castra/known_hosts
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
		KnownHosts: filepath.Join(dir, "known_hosts"),
		ClientKey:  filepath.Join(dir, "id_ed25519"),
		UserConfig: filepath.Join(sshDir, "config"),
	}, nil
}

func (p Paths) ensureDirs() error {
	if err := os.MkdirAll(p.SSHDir, 0o700); err != nil {
		return err
	}
	return os.MkdirAll(p.Dir, 0o700)
}

// EnsureClientKey creates castra's client key pair once and returns the
// public half in authorized_keys format.
func (p Paths) EnsureClientKey() ([]byte, error) {
	pubPath := p.ClientKey + ".pub"
	if pub, err := os.ReadFile(pubPath); err == nil {
		if _, err := os.Stat(p.ClientKey); err == nil {
			return pub, nil
		}
	}
	if err := p.ensureDirs(); err != nil {
		return nil, err
	}
	privPEM, pub, err := NewKey("castra")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(p.ClientKey, privPEM, 0o600); err != nil {
		return nil, err
	}
	line := ssh.MarshalAuthorizedKey(pub)
	if err := os.WriteFile(pubPath, line, 0o644); err != nil {
		return nil, err
	}
	return line, nil
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

// PinHostKey records key as the only valid host key for alias, replacing any
// earlier one (a recreated container gets a new key).
func (p Paths) PinHostKey(alias string, key ssh.PublicKey) error {
	if err := p.ensureDirs(); err != nil {
		return err
	}
	lines, err := p.otherHostLines(alias)
	if err != nil {
		return err
	}
	lines = append(lines, knownhosts.Line([]string{alias}, key))
	return writeAtomic(p.KnownHosts, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// HasHostKey reports whether a host key is pinned for alias.
func (p Paths) HasHostKey(alias string) bool {
	b, err := os.ReadFile(p.KnownHosts)
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, alias+" ") {
			return true
		}
	}
	return false
}

func (p Paths) otherHostLines(alias string) ([]string, error) {
	b, err := os.ReadFile(p.KnownHosts)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var keep []string
	for _, l := range strings.Split(string(b), "\n") {
		if l == "" || strings.HasPrefix(l, alias+" ") {
			continue
		}
		keep = append(keep, l)
	}
	return keep, nil
}

// Render produces castra's ssh config: one Host block per workspace, each
// reaching the container through `castra proxy` with no network in between.
func Render(states []workspace.State, castraBin string, p Paths) string {
	var b strings.Builder
	b.WriteString("# Managed by castra. Rewritten on every `castra up`; edits are lost.\n")
	for _, s := range states {
		fmt.Fprintf(&b, "\nHost %s\n", s.Alias())
		fmt.Fprintf(&b, "  HostKeyAlias %s\n", s.Alias())
		fmt.Fprintf(&b, "  User %s\n", s.RemoteUser)
		fmt.Fprintf(&b, "  ProxyCommand %s\n", ProxyCommand(castraBin, s.Name))
		fmt.Fprintf(&b, "  IdentityFile %s\n", quote(p.ClientKey))
		b.WriteString("  IdentitiesOnly yes\n")
		fmt.Fprintf(&b, "  UserKnownHostsFile %s\n", quote(p.KnownHosts))
		b.WriteString("  StrictHostKeyChecking yes\n")
		b.WriteString("  ForwardAgent yes\n")
		b.WriteString("  ServerAliveInterval 30\n")
		b.WriteString("  LogLevel ERROR\n")
	}
	return b.String()
}

// ProxyCommand is the line OpenSSH runs to reach a workspace: castra itself,
// which execs `podman exec -i` into the container's server.
func ProxyCommand(castraBin, name string) string {
	// OpenSSH expands %-tokens in ProxyCommand before handing it to the shell.
	bin := strings.ReplaceAll(shellescape.Quote(castraBin), "%", "%%")
	return bin + " proxy " + name
}

// quote wraps a path for an ssh_config value when it contains spaces.
func quote(path string) string {
	if strings.ContainsAny(path, " \t") {
		return `"` + path + `"`
	}
	return path
}

// WriteConfig writes castra's ssh config file.
func (p Paths) WriteConfig(states []workspace.State, castraBin string) error {
	if err := p.ensureDirs(); err != nil {
		return err
	}
	return writeAtomic(p.Config, []byte(Render(states, castraBin, p)), 0o600)
}

const (
	includeBegin = "# BEGIN castra"
	includeEnd   = "# END castra"
)

// EnsureInclude adds one Include line for castra's config to the top of
// ~/.ssh/config, between markers, if it is not there yet. It reports whether
// the file changed.
func (p Paths) EnsureInclude() (bool, error) {
	if err := p.ensureDirs(); err != nil {
		return false, err
	}
	old, err := os.ReadFile(p.UserConfig)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if bytes.Contains(old, []byte(includeBegin)) {
		return false, nil
	}
	block := fmt.Sprintf("%s\nInclude %s\n%s\n\n", includeBegin, quote(p.Config), includeEnd)
	mode := os.FileMode(0o600)
	if fi, err := os.Stat(p.UserConfig); err == nil {
		mode = fi.Mode().Perm()
	}
	return true, writeAtomic(p.UserConfig, append([]byte(block), old...), mode)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".castra-tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
