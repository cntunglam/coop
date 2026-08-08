// Command coop runs your saved scripts, presets, and workflows.
package main

import (
	"fmt"
	"os"

	"github.com/cntunglam/coop/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "coop:", err)
		os.Exit(1)
	}
}
