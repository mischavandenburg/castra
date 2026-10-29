# Security

castra is small on purpose. This page says what it trusts, what it does not, and where to report a problem.

## What the container gets

While a `castra ssh` session is open, the container can use your ssh-agent. Anything running inside it can sign with your keys for as long as the session lasts, exactly as with `ssh -A` to a server. That is the feature: it is how a private dotfiles repository clones and how `git push` works inside. It also means a devcontainer you do not trust should not be run with castra, the same rule VS Code and DevPod have. When the session ends, the socket the container used is removed and nothing of yours stays behind.

`castra up` on a repository you do not trust runs untrusted code on your machine. A devcontainer.json decides what runs in the container (image, features, lifecycle commands), its `initializeCommand` runs on the host before the container exists, and its `mounts` and `runArgs` can expose any host path or, with `--network=host`, put a server started inside the container on your machine's own address. The folder you run `castra up` in is as trusted as its devcontainer.json.

## What the container never gets

- Your private keys. Only signatures travel, over the agent protocol.
- Your host's agent socket. It is never mounted into the container; castra makes a per-session socket inside instead, in a directory only your user can open.
- A network listener of castra's own. Every byte between host and container rides on `podman exec -i` (or `docker exec -i`), which the engine authenticates by ownership of its own socket. castra opens no port on the host or in the container; a port exists only when you forward one with `ssh -L` or `ssh -R`, and an `-R` listener closes with its connection.

## No keys, as in DevPod

castra handles SSH the way DevPod does for local containers. The containers run on your own machine, and the only way in is the engine's `exec`, which only you can run, so there is no network in between for anyone to impersonate:

- The server inside the container asks the client for no key. castra keeps no client key of its own.
- The server makes a fresh host key every time it starts, and the Host block tells OpenSSH not to check it (`StrictHostKeyChecking no`, `UserKnownHostsFile /dev/null`), exactly as DevPod's does.
- Port forwarding works both ways, for TCP ports and Unix sockets, as with DevPod.

The trade, the same one DevPod makes: OpenSSH cannot tell castra's server from another program in the container that takes its place. Code that can replace files in the container can already do whatever your user can do there, so the line that matters stays the one above: run castra only on devcontainers you trust.

When a session ends, every connection a process in the container still holds to the forwarded agent is closed with it, and a program whose client went away gets SIGHUP, as a terminal hangup would send it. A program that ignores SIGHUP (`nohup`, a daemon) keeps running, by its own choice.

## What castra writes

On your machine: `~/.ssh/castra/` (one `hosts/<workspace>.conf` Host block per workspace and a `config` that includes them), `~/.local/state/castra/` (one record and one lock file per workspace), and one `Include` line at the top of `~/.ssh/config`, between `# BEGIN castra` and `# END castra` markers. If `~/.ssh/config` is a symlink, castra writes through to its target. It reads `~/.config/castra/config.yaml`.

In the container: `~/.castra/` in the remote user's home, holding the castra binary and the resolved `remoteEnv`.

## Inputs castra does not trust

A devcontainer.json decides the remote user, and the user name ends up in the Host block. Every value is encoded at the one place that writes ssh_config: control characters and double quotes are refused, whitespace is quoted, and `%` is escaped where OpenSSH expands tokens. Workspace names are reduced to `a-z`, `0-9` and `-`. Scripts castra runs inside the container are fixed strings that take their values as arguments; nothing is spliced into shell source. The one place a value must become shell text, the command line OpenSSH hands to the remote shell, goes through `internal/shell`, which has a test that reads each quoted word back through `sh`.

## Dependencies

Direct dependencies are listed in `docs/dependencies.txt`, and CI fails when `go.mod` differs from that list, so nothing is added without a decision. The SSH protocol comes from `golang.org/x/crypto`, maintained by the Go team. `govulncheck` runs on every push. Everything else that touches untrusted input is the standard library.

## Reporting a problem

Open an issue. If the problem could put users at risk, say only that you have a security report and how to reach you; the details move to a private channel from there, so a fix can land before they are public.
