package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"al.essio.dev/pkg/shellescape"
	"github.com/gofrs/flock"
	"github.com/spf13/cobra"

	"github.com/mischavandenburg/castra/internal/config"
	"github.com/mischavandenburg/castra/internal/devcontainer"
	"github.com/mischavandenburg/castra/internal/engine"
	"github.com/mischavandenburg/castra/internal/sshconfig"
	"github.com/mischavandenburg/castra/internal/workspace"
)

type upFlags struct {
	name            string
	engine          string
	recreate        bool
	dotfiles        string
	dotfilesRefresh bool
}

func newUpCmd() *cobra.Command {
	var f upFlags
	cmd := &cobra.Command{
		Use:   "up [folder | workspace]",
		Short: "Build and start the devcontainer for a folder (castra ponere: pitch camp)",
		Long: `Build and start the devcontainer for a folder, using its .devcontainer configuration.
The argument is a folder (default: the current one) or the name of a workspace castra already knows.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := "."
			if len(args) == 1 {
				target = args[0]
			}
			_, err := up(cmd.Context(), target, f, cmd.ErrOrStderr())
			return err
		},
	}
	cmd.Flags().StringVar(&f.name, "name", "", "workspace name (default: the folder's name)")
	cmd.Flags().StringVar(&f.engine, "engine", "", "container engine: podman or docker (default: podman if installed)")
	cmd.Flags().BoolVar(&f.recreate, "recreate", false, "remove the container and build a new one")
	cmd.Flags().StringVar(&f.dotfiles, "dotfiles", "", "dotfiles repository to clone into ~/dotfiles (overrides the config file)")
	cmd.Flags().BoolVar(&f.dotfilesRefresh, "dotfiles-refresh", false, "pull and reinstall dotfiles even if ~/dotfiles exists")
	return cmd
}

// resolveTarget turns the `up` argument into a folder and a workspace name.
func resolveTarget(target, name string) (string, string, error) {
	if fi, err := os.Stat(target); err == nil && fi.IsDir() {
		folder, err := filepath.Abs(target)
		if err != nil {
			return "", "", err
		}
		if name == "" {
			if name, err = workspace.NameFromFolder(folder); err != nil {
				return "", "", err
			}
		} else if name, err = workspace.CleanName(name); err != nil {
			return "", "", err
		}
		return folder, name, nil
	}
	st, err := workspace.Load(target)
	if err != nil {
		return "", "", fmt.Errorf("%q is neither a folder nor a known workspace", target)
	}
	return st.Folder, st.Name, nil
}

func up(ctx context.Context, target string, f upFlags, log io.Writer) (workspace.State, error) {
	folder, name, err := resolveTarget(target, f.name)
	if err != nil {
		return workspace.State{}, err
	}
	cfg, err := config.Load()
	if err != nil {
		return workspace.State{}, err
	}
	eng, err := engine.Detect(firstNonEmpty(f.engine, cfg.Engine))
	if err != nil {
		return workspace.State{}, err
	}
	cli, err := devcontainer.FindCLI(ctx)
	if err != nil {
		return workspace.State{}, err
	}
	paths, err := sshconfig.DefaultPaths()
	if err != nil {
		return workspace.State{}, err
	}

	lockPath, err := workspace.LockPath(name)
	if err != nil {
		return workspace.State{}, err
	}
	lock := flock.New(lockPath)
	if err := lock.Lock(); err != nil {
		return workspace.State{}, fmt.Errorf("lock %s: %w", lockPath, err)
	}
	defer func() { _ = lock.Unlock() }()

	fmt.Fprintf(log, "castra: pitching camp for %s from %s with %s\n", name, folder, eng.Name)
	res, err := devcontainer.Up(ctx, devcontainer.UpOptions{
		CLI: cli.Path, EnginePath: eng.Path, Folder: folder, Name: name, Recreate: f.recreate, Stderr: log,
	})
	if err != nil {
		return workspace.State{}, err
	}

	st := workspace.State{
		Name: name, Folder: folder, Engine: eng.Name,
		ContainerID: res.ContainerID, RemoteUser: res.RemoteUser, Workdir: res.RemoteWorkspaceFolder,
	}
	if st.RemoteHome, err = remoteHome(ctx, eng, st); err != nil {
		return st, err
	}
	if err := installServer(ctx, eng, st, log); err != nil {
		return st, err
	}
	if err := writeRemoteEnv(ctx, eng, st, res.MergedConfiguration.RemoteEnv); err != nil {
		return st, err
	}
	prev, prevErr := workspace.Load(name)
	if prevErr != nil || prev.ContainerID != st.ContainerID || !paths.HasHostKey(st.Alias()) || !remoteHasHostKey(ctx, eng, st) {
		if err := rotateHostKey(ctx, eng, st, paths); err != nil {
			return st, err
		}
	}
	if _, err := paths.EnsureClientKey(); err != nil {
		return st, err
	}
	if err := workspace.Save(st); err != nil {
		return st, err
	}
	if err := writeSSHConfig(paths); err != nil {
		return st, err
	}
	if changed, err := paths.EnsureInclude(); err != nil {
		return st, err
	} else if changed {
		fmt.Fprintf(log, "castra: added an Include for %s to %s\n", paths.Config, paths.UserConfig)
	}

	if repo := firstNonEmpty(f.dotfiles, cfg.Dotfiles.Repo); repo != "" {
		if err := installDotfiles(ctx, st, paths, repo, cfg.Dotfiles.Install, f.dotfilesRefresh, log); err != nil {
			return st, err
		}
	} else if prevErr != nil || prev.ContainerID != st.ContainerID {
		cfgPath, _ := config.Path()
		fmt.Fprintf(log, "castra: no dotfiles configured; to install yours in every new container, add to %s:\n  dotfiles:\n    repo: git@github.com:you/dotfiles.git\n", cfgPath)
	}
	fmt.Fprintf(log, "castra: %s is up. Connect with `castra ssh %s` or `ssh %s`\n", name, name, st.Alias())
	return st, nil
}

func writeSSHConfig(paths sshconfig.Paths) error {
	all, err := workspace.All()
	if err != nil {
		return err
	}
	self, err := executable()
	if err != nil {
		return err
	}
	return paths.WriteConfig(all, self)
}

func executable() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(self)
}

// remoteHome is the remote user's home directory inside the container.
func remoteHome(ctx context.Context, eng engine.Engine, st workspace.State) (string, error) {
	out, err := eng.Exec(ctx, st.RemoteUser, st.ContainerID, nil, "sh", "-c",
		`h=$(awk -F: -v u="$(id -u)" '$3==u {print $6; exit}' /etc/passwd 2>/dev/null); printf %s "${h:-$HOME}"`)
	if err != nil {
		return "", err
	}
	home := strings.TrimSpace(string(out))
	if home == "" {
		return "", fmt.Errorf("could not find the home directory of %s in the container", st.RemoteUser)
	}
	return home, nil
}

func castraDir(st workspace.State) string     { return st.RemoteHome + "/.castra" }
func serverPath(st workspace.State) string    { return castraDir(st) + "/bin/castra" }
func hostKeyPath(st workspace.State) string   { return castraDir(st) + "/host_key" }
func remoteEnvPath(st workspace.State) string { return castraDir(st) + "/remote-env.json" }

// writeRemoteEnv stores devcontainer.json's resolved remoteEnv where the
// server reads it for every session. It is rewritten on each `up`, so an
// edited remoteEnv applies after the next `castra up`.
func writeRemoteEnv(ctx context.Context, eng engine.Engine, st workspace.State, env map[string]*string) error {
	if env == nil {
		env = map[string]*string{}
	}
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	q := shellescape.Quote
	path := remoteEnvPath(st)
	script := fmt.Sprintf("set -e; mkdir -p %s; cat > %s; mv -f %s %s", q(castraDir(st)), q(path+".new"), q(path+".new"), q(path))
	_, err = eng.Exec(ctx, st.RemoteUser, st.ContainerID, bytes.NewReader(data), "sh", "-c", script)
	return err
}

// installServer streams castra's own binary into the container once per
// build of castra; the container's copy runs `castra server` for each session.
func installServer(ctx context.Context, eng engine.Engine, st workspace.State, log io.Writer) error {
	if runtime.GOOS != "linux" {
		return errors.New("this castra build cannot run inside a Linux container; macOS support is tracked in issue #1")
	}
	out, err := eng.Exec(ctx, st.RemoteUser, st.ContainerID, nil, "uname", "-m")
	if err != nil {
		return err
	}
	if arch := goArch(strings.TrimSpace(string(out))); arch != runtime.GOARCH {
		return fmt.Errorf("the container is %s and this castra is %s; mixed architectures need a release build", arch, runtime.GOARCH)
	}
	self, err := executable()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])

	bin := serverPath(st)
	have, _ := eng.Exec(ctx, st.RemoteUser, st.ContainerID, nil, "sh", "-c", "cat "+shellescape.Quote(bin+".sha256")+" 2>/dev/null || true")
	if strings.TrimSpace(string(have)) == hash {
		return nil
	}
	fmt.Fprintf(log, "castra: installing the castra server in the container (%s)\n", bin)
	q := shellescape.Quote
	script := fmt.Sprintf("set -e; mkdir -p %s; cat > %s; chmod 755 %s; mv -f %s %s; printf %%s %s > %s",
		q(filepath.Dir(bin)), q(bin+".new"), q(bin+".new"), q(bin+".new"), q(bin), hash, q(bin+".sha256"))
	_, err = eng.Exec(ctx, st.RemoteUser, st.ContainerID, bytes.NewReader(data), "sh", "-c", script)
	return err
}

func goArch(uname string) string {
	switch uname {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	default:
		return uname
	}
}

func remoteHasHostKey(ctx context.Context, eng engine.Engine, st workspace.State) bool {
	_, err := eng.Exec(ctx, st.RemoteUser, st.ContainerID, nil, "test", "-s", hostKeyPath(st))
	return err == nil
}

// rotateHostKey gives the container a new host key and pins its public half
// on the host, so OpenSSH can check it with StrictHostKeyChecking on.
func rotateHostKey(ctx context.Context, eng engine.Engine, st workspace.State, paths sshconfig.Paths) error {
	priv, pub, err := sshconfig.NewKey("castra-host-" + st.Name)
	if err != nil {
		return err
	}
	q := shellescape.Quote
	key := hostKeyPath(st)
	script := fmt.Sprintf("set -e; umask 077; mkdir -p %s; cat > %s; mv -f %s %s", q(castraDir(st)), q(key+".new"), q(key+".new"), q(key))
	if _, err := eng.Exec(ctx, st.RemoteUser, st.ContainerID, bytes.NewReader(priv), "sh", "-c", script); err != nil {
		return err
	}
	return paths.PinHostKey(st.Alias(), pub)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
