//go:build bench

// Command floors sets the bench's floors from the reports of two or more
// runs on the same commit, scored by the same judge, as bench.FloorsFrom
// does, and writes them where the bench reads them.
//
//	go run -tags bench ./bench/cmd/floors -out bench/floors.json bench/results/a.json bench/results/b.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/home-operations/kritika/bench"
)

func main() {
	out := flag.String("out", "bench/floors.json", "where to write the floors")
	flag.Parse()
	if flag.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: floors [-out file] report.json report.json...")
		os.Exit(2)
	}
	runs := make([]bench.Report, 0, flag.NArg())
	for _, path := range flag.Args() {
		raw, err := os.ReadFile(path)
		if err != nil {
			fail(err)
		}
		var r bench.Report
		if err := json.Unmarshal(raw, &r); err != nil {
			fail(fmt.Errorf("%s: %w", path, err))
		}
		runs = append(runs, r)
	}
	floors, err := bench.FloorsFrom(runs...)
	if err != nil {
		fail(err)
	}
	raw, err := json.MarshalIndent(floors, "", "  ")
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(*out, append(raw, '\n'), 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("floors from %d runs written to %s\n", len(runs), *out)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
