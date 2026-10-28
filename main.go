// castra builds devcontainers on Podman or Docker and reaches them over SSH
// with the host's ssh-agent forwarded per session.
package main

import (
	"os"

	"github.com/mischavandenburg/castra/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
