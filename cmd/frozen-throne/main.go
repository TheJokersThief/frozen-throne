// Command frozen-throne is the merge-freeze server.
package main

import (
	"flag"
	"fmt"
	"os"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	check := flag.Bool("healthcheck", false, "probe /healthz on the local listener and exit 0/1 (for container healthchecks)")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if *check {
		if err := healthcheck(); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		return
	}
	// Settings come from FT_* environment variables (see docs/configuration.md).
	if err := serve(); err != nil {
		fmt.Fprintln(os.Stderr, "frozen-throne:", err)
		os.Exit(1)
	}
}
