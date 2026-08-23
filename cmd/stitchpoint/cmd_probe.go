package main

import (
	"fmt"

	"github.com/izaacledererjunior/stitchpoint/internal/probe"
)

// runProbe implements the `stitchpoint probe` subcommand: print a media
// file's duration and video keyframe timestamps via internal/probe's
// direct libavformat bindings.
func runProbe(args []string) error {
	if len(args) != 1 || args[0] == "-h" || args[0] == "--help" {
		return fmt.Errorf("usage: stitchpoint probe <file>")
	}
	path := args[0]

	duration, err := probe.Duration(path)
	if err != nil {
		return err
	}
	fmt.Printf("duration: %v\n", duration)

	keyframes, err := probe.Keyframes(path)
	if err != nil {
		return err
	}
	fmt.Printf("keyframes: %d\n", len(keyframes))
	for i, kf := range keyframes {
		fmt.Printf("  [%d] %v\n", i, kf.PTS)
	}
	return nil
}
