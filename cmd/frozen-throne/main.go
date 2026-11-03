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
	// Wired into newReconciler once the server exists.
	_ = flag.Duration("reconcile-interval", defaultReconcileInterval, "how often to re-check every repo to heal missed webhooks")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	fmt.Fprintln(os.Stderr, "frozen-throne: server not implemented yet (scaffold)")
	os.Exit(1)
}
