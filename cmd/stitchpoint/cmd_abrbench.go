package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/abrbench"
)

// runABRBench implements the `stitchpoint abr-bench` subcommand: encode
// the input at every rung of the default ABR ladder and print how closely
// each rung's actual output bitrate tracked its target.
func runABRBench(args []string) error {
	fs := flag.NewFlagSet("abr-bench", flag.ExitOnError)
	outDir := fs.String("out", "", "output directory for the per-rung encoded files; required")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: stitchpoint abr-bench -out <dir> <input>")
	}
	if *outDir == "" {
		return fmt.Errorf("-out is required")
	}
	input := fs.Arg(0)

	results, err := abrbench.Run(input, *outDir, abrbench.DefaultLadder)
	if err != nil {
		return err
	}

	fmt.Printf("%-8s %-11s %10s %10s %8s %10s\n", "RUNG", "RESOLUTION", "TARGET", "ACTUAL", "DELTA", "ENCODE")
	for _, r := range results {
		fmt.Printf("%-8s %-11s %8dk %8.0fk %+6.1f%% %10s\n",
			r.Rung.Name,
			fmt.Sprintf("%dx%d", r.Rung.Width, r.Rung.Height),
			r.TargetBitrateKbps(),
			r.ActualBitrateKbps,
			r.DeltaPercent(),
			r.EncodeWallTime.Round(time.Millisecond),
		)
	}
	return nil
}
