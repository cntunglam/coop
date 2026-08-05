// Command rx runs your saved scripts, presets, and workflows.
package main

import (
	"fmt"
	"os"

	"github.com/caongoctunglam/rx/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "rx:", err)
		os.Exit(1)
	}
}
