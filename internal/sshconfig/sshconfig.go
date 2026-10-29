// Package sshconfig owns castra's files under ~/.ssh/castra: one Host block
// per workspace and the config that includes them, plus the one Include line
// that makes OpenSSH read castra's config. Each workspace's block is its own
// file, so two `castra up` on different folders never touch the same file.
package sshconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
		UserConfig: filepath.Join(sshDir, "config"),
	}, nil
}

// HostFile is the workspace's Host block.
func (p Paths) HostFile(name string) string { return filepath.Join(p.HostsDir, name+".conf") }

func (p Paths) ensureDirs() error {
	for _, d := range []string{p.SSHDir, p.Dir, p.HostsDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// locked runs fn while holding the lock on the files every workspace shares:
// castra's config and the Include in ~/.ssh/config.
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
	block, err := Render(s, castraBin)
	if err != nil {
		return err
	}
	return atomic.WriteFile(p.HostFile(s.Name), []byte(block), 0o600)
}

// RemoveHost removes the workspace's Host block.
func (p Paths) RemoveHost(s workspace.State) error {
	if err := os.Remove(p.HostFile(s.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Render produces the workspace's Host block: DevPod's, line for line, with
// castra's ProxyCommand, plus a keepalive so an idle session survives. The
// host key is not checked and no key file is named, as in DevPod: the
// connection is an `exec` into your own container, with no network to
// impersonate anyone on. Every value is encoded for ssh_config here, at the
// one place that writes it.
func Render(s workspace.State, castraBin string) (string, error) {
	user, err := value(s.RemoteUser)
	if err != nil {
		return "", fmt.Errorf("remote user: %w", err)
	}
	proxy, err := ProxyCommand(castraBin, s.Name)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Managed by castra for workspace %s. Rewritten on `castra up`; edits are lost.\n", s.Name)
	fmt.Fprintf(&b, "Host %s\n", s.Alias())
	b.WriteString("  ForwardAgent yes\n")
	b.WriteString("  LogLevel error\n")
	b.WriteString("  StrictHostKeyChecking no\n")
	b.WriteString("  UserKnownHostsFile /dev/null\n")
	fmt.Fprintf(&b, "  ProxyCommand %s\n", proxy)
	fmt.Fprintf(&b, "  User %s\n", user)
	b.WriteString("  ServerAliveInterval 30\n")
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

// pathValue is value for keywords that expand %-tokens (Include).
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

// legacy are the files of earlier castra builds that pinned keys: one shared
// known_hosts (first layout), per-workspace pins (second), and castra's own
// client key (both). castra reads none of them any more.
var legacy = []string{"known_hosts", "known_hosts.d", "id_ed25519", "id_ed25519.pub"}

// Migrate brings the files of an earlier castra build to this layout: every
// workspace's Host block rewritten, castra's config an Include of them, and
// the key and pin files removed. It costs a few stats once there is nothing
// left to migrate.
func (p Paths) Migrate(load func() ([]workspace.State, error), castraBin string) error {
	if !p.hasLegacy() {
		return nil
	}
	return p.locked(func() error {
		if !p.hasLegacy() {
			return nil // another run migrated while we waited for the lock
		}
		states, err := load()
		if err != nil {
			return err
		}
		for _, s := range states {
			if err := p.writeHost(s, castraBin); err != nil {
				return err
			}
		}
		if err := p.ensureConfig(); err != nil {
			return err
		}
		for _, name := range legacy {
			if err := os.RemoveAll(filepath.Join(p.Dir, name)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (p Paths) hasLegacy() bool {
	for _, name := range legacy {
		if _, err := os.Lstat(filepath.Join(p.Dir, name)); err == nil {
			return true
		}
	}
	return false
}
