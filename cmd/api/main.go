// Command ants-api is the production API server entrypoint. Configuration
// comes from ANTS_CONFIG or --config; secrets come from the environment.
package main

import (
	"os"

	"github.com/metaforismo/ants/internal/cli"
)

func main() {
	os.Exit(cli.RunServe(os.Args[1:], os.Stdout, os.Stderr))
}
