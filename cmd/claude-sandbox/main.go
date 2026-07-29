// Command claude-sandbox is the sandbox launcher and (in-container) the ralph
// loop runner. Invoked as "ralph" (argv0 or symlink) it behaves as the ralph
// subcommand for the container entrypoint's benefit.
package main

import (
	"os"
	"path/filepath"
)

func main() {
	args := os.Args[1:]
	if filepath.Base(os.Args[0]) == "ralph" {
		args = append([]string{"ralph"}, args...)
	}
	os.Exit(Main(args))
}
