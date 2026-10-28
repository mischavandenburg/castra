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
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

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
	held, err := workspace.Lock(name)
	if err != nil {
		return workspace.State{}, err
	}
	defer func() { _ = held.Release() }()

	cfg, err := config.Load()
	if err != nil {
		return workspace.State{}, err
	}
	// An existing workspace stays on the engine it was created with; moving
	// it would leave its container behind on the old engine.
	prev, prevErr := workspace.Load(name)
	if prevErr == nil && f.engine != "" && f.engine != prev.Engine {
		return workspace.State{}, fmt.Errorf("workspace %s runs on %s; to move it to %s, `castra delete %s` first", name, prev.Engine, f.engine, name)
	}
	eng, err := engine.Detect(firstNonEmpty(f.engine, prev.Engine, cfg.Engine))
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
	self, err := executable()
	if err != nil {
		return workspace.State{}, err
	}
	if err := paths.Migrate(workspace.All, self); err != nil {
		return workspace.State{}, err
	}

	fmt.Fprintf(log, "castra: pitching camp for %s from %s with %s\n", name, folder, eng.Name)
	// Ctrl-C while the devcontainer CLI builds cancels this context, and Up
	// then ends the CLI's whole process group. Everywhere else castra dies on
	// the signal the ordinary way.
	buildCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	res, err := devcontainer.Up(buildCtx, devcontainer.UpOptions{
		CLI: cli.Path, EnginePath: eng.Path, Folder: folder, Name: name, Recreate: f.recreate, Stderr: log,
	})
	stop()
	if err != nil {
		return workspace.State{}, err
	}

	st := workspace.State{
		Name: name, Folder: folder, Engine: eng.Name,
		ContainerID: res.ContainerID, RemoteUser: res.RemoteUser, Workdir: res.RemoteWorkspaceFolder,
	}
	fresh := prevErr != nil || prev.ContainerID != st.ContainerID
	facts, err := probe(ctx, eng, st)
	if err != nil {
		return st, err
	}
	st.RemoteHome = facts.home
	if err := installServer(ctx, eng, st, facts, self, log); err != nil {
		return st, err
	}
	if err := writeRemoteEnv(ctx, eng, st, res.MergedConfiguration.RemoteEnv); err != nil {
		return st, err
	}
	if fresh || !paths.HasHostKey(st) || !facts.hostKey {
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
	if err := paths.WriteHost(st, self); err != nil {
		return st, err
	}
	if changed, err := paths.EnsureInclude(); err != nil {
		return st, err
	} else if changed {
		fmt.Fprintf(log, "castra: added an Include for %s to %s\n", paths.Config, paths.UserConfig)
	}

	repo := firstNonEmpty(f.dotfiles, cfg.Dotfiles.Repo)
	switch {
	case repo == "":
		if fresh {
			cfgPath, _ := config.Path()
			fmt.Fprintf(log, "castra: no dotfiles configured; to install yours in every new container, add to %s:\n  dotfiles:\n    repo: git@github.com:you/dotfiles.git\n", cfgPath)
		}
	case facts.dotfiles && !f.dotfilesRefresh:
		// Already installed: nothing to clone, so no SSH session to open.
		fmt.Fprintf(log, "castra: dotfiles already in %s/%s, skipping\n", st.RemoteHome, remoteDotfiles)
	default:
		if err := installDotfiles(ctx, st, paths, repo, cfg.Dotfiles.Install, f.dotfilesRefresh, log); err != nil {
			return st, err
		}
	}
	fmt.Fprintf(log, "castra: %s is up. Connect with `castra ssh %s` or `ssh %s`\n", name, name, st.Alias())
	return st, nil
}

func executable() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(self)
}

// castra's files in the remote user's home. The probe passes these names to
// its script as arguments, so the layout lives here and nowhere else.
const (
	remoteBin      = ".castra/bin/castra"
	remoteHostKey  = ".castra/host_key"
	remoteEnvFile  = ".castra/remote-env.json"
	remoteDotfiles = "dotfiles"
)

func serverPath(st workspace.State) string    { return st.RemoteHome + "/" + remoteBin }
func hostKeyPath(st workspace.State) string   { return st.RemoteHome + "/" + remoteHostKey }
func remoteEnvPath(st workspace.State) string { return st.RemoteHome + "/" + remoteEnvFile }

// facts is what one probe of the container tells castra.
type facts struct {
	home      string // the remote user's home directory
	arch      string // GOARCH of the container
	serverSum string // sha256 of the installed server, or ""
	hostKey   bool   // the host key is in place
	dotfiles  bool   // ~/dotfiles is a git checkout
}

// probe asks the container everything castra needs to know in one exec.
// Paths are relative to the home and arrive as arguments, never spliced in.
func probe(ctx context.Context, eng engine.Engine, st workspace.State) (facts, error) {
	const script = `h=$(awk -F: -v u="$(id -u)" '$3==u {print $6; exit}' /etc/passwd 2>/dev/null); h="${h:-$HOME}"
cd "$h" || exit 1
printf '%s\n' "$h"
uname -m
cat "$1.sha256" 2>/dev/null; echo
test -s "$2" && echo yes || echo no
test -d "$3/.git" && echo yes || echo no`
	out, err := eng.Exec(ctx, st.RemoteUser, st.ContainerID, nil, "sh", "-c", script, "sh", remoteBin, remoteHostKey, remoteDotfiles)
	if err != nil {
		return facts{}, err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) != 5 || lines[0] == "" {
		return facts{}, fmt.Errorf("could not read the container's user, home and state (%q)", out)
	}
	return facts{
		home:      lines[0],
		arch:      goArch(strings.TrimSpace(lines[1])),
		serverSum: strings.TrimSpace(lines[2]),
		hostKey:   lines[3] == "yes",
		dotfiles:  lines[4] == "yes",
	}, nil
}

// putRemote writes data to an absolute path inside the container, in one
// step and with the given mode. The path and mode are arguments to a fixed
// script; nothing from castra is spliced into shell source.
func putRemote(ctx context.Context, eng engine.Engine, st workspace.State, path, mode string, data []byte) error {
	const script = `set -e; umask 077; mkdir -p "${1%/*}"; cat > "$1.new"; chmod "$2" "$1.new"; mv -f "$1.new" "$1"`
	_, err := eng.Exec(ctx, st.RemoteUser, st.ContainerID, bytes.NewReader(data), "sh", "-c", script, "sh", path, mode)
	return err
}

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
	return putRemote(ctx, eng, st, remoteEnvPath(st), "600", data)
}

// installServer streams castra's own binary into the container once per
// build of castra; the container's copy runs `castra server` for each session.
func installServer(ctx context.Context, eng engine.Engine, st workspace.State, f facts, self string, log io.Writer) error {
	if runtime.GOOS != "linux" {
		return errors.New("this castra build cannot run inside a Linux container; macOS support is tracked in issue #1")
	}
	if f.arch != runtime.GOARCH {
		return fmt.Errorf("the container is %s and this castra is %s; mixed architectures need a release build", f.arch, runtime.GOARCH)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	if f.serverSum == hash {
		return nil
	}
	bin := serverPath(st)
	fmt.Fprintf(log, "castra: installing the castra server in the container (%s)\n", bin)
	if err := putRemote(ctx, eng, st, bin, "755", data); err != nil {
		return err
	}
	return putRemote(ctx, eng, st, bin+".sha256", "600", []byte(hash))
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

// rotateHostKey gives the container a new host key and pins its public half
// on the host, so OpenSSH can check it with StrictHostKeyChecking on.
func rotateHostKey(ctx context.Context, eng engine.Engine, st workspace.State, paths sshconfig.Paths) error {
	priv, pub, err := sshconfig.NewKey("castra-host-" + st.Name)
	if err != nil {
		return err
	}
	if err := putRemote(ctx, eng, st, hostKeyPath(st), "600", priv); err != nil {
		return err
	}
	return paths.PinHostKey(st, pub)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
