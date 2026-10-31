// Package config reads ~/.config/castra/config.yaml. Every field is optional.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

// Config is the user's castra settings.
type Config struct {
	// Engine is "podman" or "docker". Empty means podman if present, else docker.
	Engine   string   `json:"engine,omitempty"`
	Dotfiles Dotfiles `json:"dotfiles,omitempty"`
}

// Dotfiles is the repository castra clones into ~/dotfiles of every new
// workspace, over the forwarded agent, and the command that installs it.
type Dotfiles struct {
	Repo string `json:"repo,omitempty"`
	// Install runs inside ~/dotfiles. Empty means the first of install.sh,
	// install, bootstrap.sh, bootstrap, setup.sh, setup that exists.
	Install string `json:"install,omitempty"`
}

// Path is the config file location. A relative $XDG_CONFIG_HOME is ignored
// because the XDG Base Directory spec says such a path is invalid.
func Path() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); filepath.IsAbs(d) {
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
		return Config{}, fmt.Errorf("%s: %s", p, cleanError(err))
	}
	return c, nil
}

// cleanError rewrites the YAML library's error in castra's words. It removes
// most of the library's JSON wording, but not all: a NaN or Inf value under
// an unknown key can still surface "json: unsupported value: NaN".
func cleanError(err error) string {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) && typeErr.Field != "" {
		return fmt.Sprintf("wrong type for setting %q (got %s)", typeErr.Field, typeErr.Value)
	}
	msg := err.Error()
	if rest, ok := strings.CutPrefix(msg, "error converting YAML to JSON: "); ok {
		msg = rest
	} else if rest, ok := strings.CutPrefix(msg, "error unmarshaling JSON: while decoding JSON: json: "); ok {
		msg = rest
		if field, ok := strings.CutPrefix(msg, "unknown field "); ok {
			msg = "unknown setting " + field
		}
	}
	return msg
}
