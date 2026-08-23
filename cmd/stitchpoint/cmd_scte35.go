package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/izaacledererjunior/stitchpoint/internal/hls"
	"github.com/izaacledererjunior/stitchpoint/internal/mpd"
	"github.com/izaacledererjunior/stitchpoint/internal/scte35"
)

// runSCTE35 implements the `stitchpoint scte35` subcommand: decode each
// input cue and print a one-line summary via scte35.Describe. Malformed
// cues are reported and skipped rather than aborting the whole batch.
func runSCTE35(args []string) error {
	fs := flag.NewFlagSet("scte35", flag.ExitOnError)
	filePath := fs.String("file", "", "read cues (one base64 or hex string per line) from this file instead of stdin")
	manifestPath := fs.String("manifest", "", "extract and decode every SCTE-35 cue found in this HLS playlist (.m3u8)")
	mpdPath := fs.String("mpd", "", "extract and decode every SCTE-35 cue found in this DASH MPD's EventStream elements (urn:scte:scte35:2013:xml scheme only — see internal/mpd's package doc)")
	segmentPath := fs.String("segment", "", "extract and decode every SCTE-35 cue found inband in this DASH media segment's emsg boxes (.m4s; urn:scte:scte35:2013:bin scheme only — see internal/mpd's package doc)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *manifestPath != "" {
		return runSCTE35Manifest(*manifestPath)
	}
	if *mpdPath != "" {
		return runSCTE35MPD(*mpdPath)
	}
	if *segmentPath != "" {
		return runSCTE35Segment(*segmentPath)
	}

	var cues []string
	switch {
	case fs.NArg() > 0:
		cues = fs.Args()
	case *filePath != "":
		f, err := os.Open(*filePath)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		cues, err = readLines(f)
		if err != nil {
			return err
		}
	default:
		var err error
		cues, err = readLines(os.Stdin)
		if err != nil {
			return err
		}
	}

	if len(cues) == 0 {
		return fmt.Errorf("no cues given (pass as arguments, -file, -manifest, or via stdin)")
	}

	exitStatus := 0
	for i, cue := range cues {
		section, err := decodeCue(cue)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stitchpoint: cue %d: %v\n", i+1, err)
			exitStatus = 1
			continue
		}
		fmt.Println(scte35.Describe(section))
	}

	if exitStatus != 0 {
		os.Exit(exitStatus)
	}
	return nil
}

// runSCTE35Manifest handles the -manifest path: extract every cue
// reference from the playlist, then decode and describe each one,
// prefixed with its source line and tag.
func runSCTE35Manifest(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	cues, err := hls.ExtractCues(f)
	if err != nil {
		return fmt.Errorf("reading manifest: %w", err)
	}
	if len(cues) == 0 {
		return fmt.Errorf("no SCTE-35 cues found in %s", path)
	}

	exitStatus := 0
	for _, cue := range cues {
		section, err := cue.Decode()
		if err != nil {
			fmt.Fprintf(os.Stderr, "stitchpoint: line %d (%s): %v\n", cue.Line, cue.Tag, err)
			exitStatus = 1
			continue
		}
		fmt.Printf("line %d (%s): %s\n", cue.Line, cue.Tag, scte35.Describe(section))
	}

	if exitStatus != 0 {
		os.Exit(exitStatus)
	}
	return nil
}

// runSCTE35MPD handles the -mpd path: extract every SCTE-35 EventStream
// cue from the DASH manifest and describe each one, same as -manifest
// does for HLS.
func runSCTE35MPD(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	doc, err := mpd.Parse(f)
	if err != nil {
		return fmt.Errorf("parsing MPD: %w", err)
	}
	cues, err := mpd.ExtractCues(doc)
	if err != nil {
		return fmt.Errorf("extracting cues: %w", err)
	}
	if len(cues) == 0 {
		return fmt.Errorf("no SCTE-35 EventStream cues found in %s", path)
	}

	for _, cue := range cues {
		fmt.Printf("period=%s event=%s presentationTime=%s: %s\n", cue.PeriodID, cue.EventID, cue.PresentationTime, scte35.Describe(cue.SpliceInfoSection))
	}
	return nil
}

// runSCTE35Segment handles the -segment path: extract every SCTE-35 cue
// carried inband via 'emsg' boxes in a DASH media segment (.m4s) — the
// counterpart to -mpd's out-of-band EventStream cues.
func runSCTE35Segment(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	cues, err := mpd.ExtractEmsgCues(f)
	if err != nil {
		return fmt.Errorf("reading segment: %w", err)
	}
	if len(cues) == 0 {
		return fmt.Errorf("no SCTE-35 emsg cues found in %s", path)
	}

	for _, cue := range cues {
		duration := "unknown"
		if !cue.EventDurationUnknown {
			duration = cue.EventDuration.String()
		}
		fmt.Printf("emsg id=%d v%d presentationTime=%s duration=%s: %s\n", cue.ID, cue.Version, cue.PresentationTime, duration, scte35.Describe(cue.SpliceInfoSection))
	}
	return nil
}

// decodeCue tries base64 first (the form SCTE-35 takes in HLS manifests and
// most ad-decision APIs) and falls back to hex, so callers don't need to
// know or specify the encoding up front.
func decodeCue(s string) (*scte35.SpliceInfoSection, error) {
	if section, err := scte35.ParseBase64(s); err == nil {
		return section, nil
	}
	return scte35.ParseHex(s)
}
