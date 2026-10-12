// Command jabali is the entry point for the Jabali Panel. It serves
// both the HTTP(S) API (`jabali serve`) and administrative CLI commands
// (`jabali user list`, `jabali system info`, `jabali migrate up`, …).
//
// Running `jabali` with no subcommand prints help. The previous bare
// `main()` that started the server directly is now behind `jabali serve`.
package main

import (
	"fmt"
	"os"
)

const defaultConfigPath = "/etc/jabali/config.toml"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
