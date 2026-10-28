# castra

castra builds a devcontainer from your project's `.devcontainer` folder, runs it on Podman (or Docker) on your own machine, and lets you in over SSH with your ssh-agent forwarded. That's all it does.

Roman legions built a camp, a *castra*, at the end of every day's march, always to the same plan, so every soldier knew exactly where his tent was (Polybius, *Histories* 6.41). castra builds the same container every time.

> **Status: early.** Tested on Linux with rootless Podman 5 (Fedora 43, SELinux enforcing). Docker and macOS are not tested yet ([#1](https://github.com/mischavandenburg/castra/issues/1)). There is no license yet ([#2](https://github.com/mischavandenburg/castra/issues/2)), which means you may read the code but not reuse it until one is chosen.

## Why

[DevPod](https://github.com/loft-sh/devpod) does this job and much more: cloud providers, Kubernetes, IDE launchers, a desktop app. It stopped being maintained in 2025. If all you do is run devcontainers locally and SSH into them, castra is that one path, kept small.

## Install

You need four things: Go 1.26 or newer, Podman (or Docker), the OpenSSH client, and Microsoft's [devcontainer CLI](https://github.com/devcontainers/cli) on your `PATH`. castra uses that CLI for all the devcontainer.json work and does not install it for you, so you update it the same way you update your other tools.

With [mise](https://mise.jdx.dev):

```console
mise use -g node@lts npm:@devcontainers/cli
go install github.com/mischavandenburg/castra@latest
castra doctor
```

Without mise, install the CLI with npm instead: `npm install -g @devcontainers/cli`.

`castra doctor` checks everything castra needs. Every `castra up` checks the CLI first: if it is missing, does not run, or is older than 0.89.0 (the oldest release castra is verified with), castra stops and tells you the command that fixes it. Upgrading is `mise up npm:@devcontainers/cli`.

## Use

```console
cd my-project            # a folder with .devcontainer/devcontainer.json
castra up                # build and start the container
castra ssh my-project    # open a shell inside it
```

The workspace is named after the folder (`my-project`), and inside the container your code is at `/workspaces/my-project`.

Leave out the name and castra shows a menu of your workspaces (arrow keys or j/k, Enter, a number to jump, q to quit). With only one workspace it goes straight in.

```console
castra ssh
```

Run a single command instead of a shell:

```console
castra ssh my-project -- git status
```

See your workspaces and whether their containers are running (`--json` for scripts):

```console
castra ls
```

Rebuild from scratch after you change the Dockerfile or devcontainer.json:

```console
castra up my-project --recreate
```

Delete a workspace when you are done with it. castra asks first (`-y` skips the question), then removes the container, its SSH host entry and pinned host key. Your folder, the images and any named volumes stay. Without a name it shows the menu.

```console
castra delete my-project
```

castra also adds a host to your SSH config, so anything that speaks SSH works too:

```console
ssh my-project.castra
scp notes.txt my-project.castra:
rsync -a ./data/ my-project.castra:/workspaces/my-project/data/
```

## Dotfiles

Put your dotfiles repository in `~/.config/castra/config.yaml`:

```yaml
dotfiles:
  repo: git@github.com:you/dotfiles.git
```

On the first `up` of each new container, castra clones it to `~/dotfiles` inside the container and runs the first of `install.sh`, `install`, `bootstrap.sh`, `bootstrap`, `setup.sh`, `setup` it finds. The clone goes over SSH with your forwarded agent, so a private repository works without copying any key into the container. Set `dotfiles.install` to run a different command instead.

## How it works

- **Building** is done by Microsoft's devcontainer CLI. Features, lifecycle commands, mounts and `runArgs` behave as the spec says.
- **SSH** runs over `podman exec -i`. castra copies itself into the container once and runs a small SSH server there for each connection. Nothing listens on a port, on the host or in the container.
- **Your agent** is forwarded per session. Inside a session, `SSH_AUTH_SOCK` points at a socket that exists only while the session does. Your host's socket is never mounted into the container.
- **Host keys** are pinned. Each container gets its own host key, and OpenSSH checks it strictly. A recreated container gets a new key, and castra updates the pin.

castra keeps its files in `~/.ssh/castra/` (its SSH config, client key and pinned host keys), `~/.local/state/castra/` (one record per workspace) and `~/.config/castra/config.yaml`. The only change it makes outside those is one `Include` line at the top of `~/.ssh/config`.

## Rootless Podman on a machine you reach over SSH

Rootless Podman stops your containers when your last login session ends. On a laptop with a desktop session you won't notice. On a server or a second machine you only SSH into, turn on lingering once:

```console
sudo loginctl enable-linger $USER
```

`castra doctor` tells you when lingering is off.

## Non-goals

castra says no to these on purpose, so it stays small:

- **Windows.** Linux and macOS only.
- **IDE integration.** No VS Code or JetBrains launching. Your editor can use the SSH host castra writes.
- **Providers.** No cloud VMs, no Kubernetes, no remote Docker hosts. Local Podman or Docker only.
- **Daemons.** Nothing runs in the background on your machine or in the container between sessions: no inactivity timeout, no auto-stop.
- **Credential forwarding.** Only your ssh-agent is forwarded. No git or Docker credential helpers.
- **Docker Compose devcontainers**, for now.
