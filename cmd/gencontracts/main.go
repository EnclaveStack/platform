// Command gencontracts writes console/src/api/generated.ts from pkg/api.
package main

import (
	"log"
	"os"

	"github.com/enclavestack/platform/internal/contracts"
)

func main() {
	if err := os.WriteFile(contracts.GeneratedPath, []byte(contracts.TypeScript()), 0o644); err != nil {
		log.Fatal(err)
	}
}
