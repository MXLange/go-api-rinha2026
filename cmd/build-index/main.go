package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"go-api-rinha2026/internal/knn"
)

func main() {
	input := flag.String("input", "resources/references.json.gz", "references.json.gz path")
	output := flag.String("out", "data/knn.idx", "output index path")
	leafSize := flag.Int("leaf-size", 48, "kd-tree leaf size")
	flag.Parse()

	start := time.Now()
	if err := knn.BuildFile(*input, *output, *leafSize); err != nil {
		fmt.Fprintf(os.Stderr, "build-index: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s in %s\n", *output, time.Since(start).Round(time.Millisecond))
}
