// Command release builds every file of a release into build/release (ADR-054): `make release VERSION=vX.Y.Z`.
// The console must be built and embedded first, which the make target does.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/enclavestack/platform/internal/release"
)

func main() {
	tag := flag.String("version", "", "the release tag, vX.Y.Z")
	out := flag.String("out", "build/release", "where the files go (emptied first)")
	epoch := flag.Int64("mtime", 0, "seconds since 1970 stamped on archived files (default: SOURCE_DATE_EPOCH, else 1980-01-01)")
	flag.Parse()
	cfg := release.Config{Tag: *tag, Root: ".", Out: *out}
	if *epoch == 0 {
		if v, ok := os.LookupEnv("SOURCE_DATE_EPOCH"); ok {
			fmt.Sscan(v, epoch)
		}
	}
	if *epoch > 0 {
		cfg.ModTime = time.Unix(*epoch, 0).UTC()
	}
	if _, err := os.Stat("internal/webui/dist/index.html"); err != nil {
		log.Fatal("the console isn't embedded: run `make release`, which builds it first")
	}
	res, err := release.Run(context.Background(), cfg)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Release %s: %d files in %s\n", res.Version, len(res.Files)+2, *out)
	for _, f := range res.Files {
		fmt.Printf("  %s  %s\n", res.Checksum[f], f)
	}
	fmt.Println("  SHA256SUMS\n  enclave.rb")
}
