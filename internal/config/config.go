// Package config reads ~/.config/castra/config.yaml. Every field is optional.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"sigs.k8s.io/yaml"
)

// Config is the user's castra settings.
type Config struct {
	// Engine is "podman" or "docker". Empty means podman if present, else docker.
	Engine   string   `json:"engine,omitempty"`
	Dotfiles Dotfiles `json:"dotfiles,omitempty"`
	// SendEnv names host environment variables passed into every session,
	// the dotfiles install included, when they are set (OpenSSH SendEnv, so
	// a trailing * matches a prefix). Example: GITHUB_TOKEN, which tools such
	// as mise need to stay under GitHub's API rate limit.
	SendEnv []string `json:"sendEnv,omitempty"`
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\*?$`)

func (c Config) validate() error {
	for _, n := range c.SendEnv {
		if !envName.MatchString(n) {
			return fmt.Errorf("sendEnv: %q is not an environment variable name", n)
		}
	}
	return nil
}

// Dotfiles is the repository castra clones into ~/dotfiles of every new
// workspace, over the forwarded agent, and the command that installs it.
type Dotfiles struct {
	Repo string `json:"repo,omitempty"`
	// Install runs inside ~/dotfiles. Empty means the first of install.sh,
	// install, bootstrap.sh, bootstrap, setup.sh, setup that exists.
	Install string `json:"install,omitempty"`
}

// Path is the config file location.
func Path() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "castra", "config.yaml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "castra", "config.yaml"), nil
}

// Load reads the config file; a missing file is an empty config.
func Load() (Config, error) {
	p, err := Path()
	if err != nil {
		return Config{}, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := yaml.UnmarshalStrict(b, &c); err != nil {
		return Config{}, fmt.Errorf("%s: %w", p, err)
	}
	if err := c.validate(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", p, err)
	}
	return c, nil
}
