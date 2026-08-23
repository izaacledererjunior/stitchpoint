package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/dashsplice"
	"github.com/izaacledererjunior/stitchpoint/internal/mpd"
	"github.com/izaacledererjunior/stitchpoint/internal/transcode"
	"github.com/izaacledererjunior/stitchpoint/internal/vast"
)

// runDashStitch implements the `stitchpoint dash-stitch` subcommand:
// DASH's equivalent of `stitch` (see internal/dashsplice). Materialization
// is simpler than stitch's: it copies each source directory wholesale
// (content into -out, ad into -out/ad/) rather than resolving each
// $Number$ template to the exact files referenced — a few unreferenced
// files left behind is a fine tradeoff for a v1 CLI wrapper.
func runDashStitch(args []string) error {
	fs := flag.NewFlagSet("dash-stitch", flag.ExitOnError)
	contentPath := fs.String("content", "", "path to the content MPD (.mpd)")
	adPath := fs.String("ad", "", "path to a pre-encoded ad MPD (.mpd)")
	vastURL := fs.String("vast", "", "VAST tag URL to resolve and encode as the ad, instead of -ad")
	outDir := fs.String("out", "", "output directory for the spliced MPD and segment files")
	manifestName := fs.String("manifest-name", "stitched.mpd", "filename for the spliced MPD inside -out")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *contentPath == "" || *outDir == "" {
		return fmt.Errorf("-content and -out are required")
	}
	if (*adPath == "") == (*vastURL == "") {
		return fmt.Errorf("exactly one of -ad or -vast is required")
	}

	content, contentDir, err := loadMPD(*contentPath)
	if err != nil {
		return err
	}

	var ad *mpd.MPD
	var adDir string
	allowMismatch := false
	if *vastURL != "" {
		ad, adDir, err = resolveDashAd(*vastURL)
		allowMismatch = true // see dashsplice.Options.AllowDurationMismatch
	} else {
		ad, adDir, err = loadMPD(*adPath)
	}
	if err != nil {
		return err
	}

	// Prefix before splicing so the spliced MPD already points at "ad/...".
	prefixMediaPaths(ad, "ad/")

	out, err := dashsplice.SpliceWithOptions(content, ad, dashsplice.Options{AllowDurationMismatch: allowMismatch})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(*outDir, 0o750); err != nil {
		return err
	}
	if err := copyDirFiles(contentDir, *outDir); err != nil {
		return fmt.Errorf("copying content segments: %w", err)
	}
	if err := copyDirFiles(adDir, filepath.Join(*outDir, "ad")); err != nil {
		return fmt.Errorf("copying ad segments: %w", err)
	}

	manifestPath := filepath.Join(*outDir, *manifestName)
	mf, err := os.Create(manifestPath)
	if err != nil {
		return err
	}
	if err := mpd.Write(mf, out); err != nil {
		_ = mf.Close()
		return err
	}
	if err := mf.Close(); err != nil {
		return err
	}

	fmt.Printf("spliced MPD: %s\n", manifestPath)
	fmt.Printf("%d periods total (1 ad period inserted)\n", len(out.Periods))
	return nil
}

// resolveDashAd mirrors resolveAd (cmd_stitch.go) but encodes via
// transcode.EncodeDASH instead of EncodeHLS.
func resolveDashAd(vastURL string) (*mpd.MPD, string, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	resolved, err := vast.Fetch(client, vastURL)
	if err != nil {
		return nil, "", fmt.Errorf("resolving VAST tag: %w", err)
	}
	mediaFile, ok := resolved.SelectMediaFile()
	if !ok {
		return nil, "", fmt.Errorf("VAST ad %q (%s) has no usable progressive MP4 MediaFile", resolved.AdTitle, resolved.AdSystem)
	}
	fmt.Printf("VAST: %q via %s, %s creative, %v duration\n", resolved.AdTitle, resolved.AdSystem, mediaFile.Type, resolved.Duration)

	tmpDir, err := os.MkdirTemp("", "stitchpoint-vast-dash-*")
	if err != nil {
		return nil, "", err
	}

	creativePath := filepath.Join(tmpDir, "creative.mp4")
	if err := transcode.DownloadFile(client, mediaFile.URL, creativePath); err != nil {
		return nil, "", fmt.Errorf("downloading creative: %w", err)
	}

	encodedDir := filepath.Join(tmpDir, "encoded")
	ad, err := transcode.EncodeDASH(creativePath, encodedDir, transcode.DefaultParams)
	if err != nil {
		return nil, "", fmt.Errorf("encoding creative to match content: %w", err)
	}
	return ad, encodedDir, nil
}

func loadMPD(path string) (*mpd.MPD, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = f.Close() }()
	m, err := mpd.Parse(f)
	if err != nil {
		return nil, "", err
	}
	return m, filepath.Dir(path), nil
}

// prefixMediaPaths rewrites every Representation's Media/Initialization
// template across every Period of m to live under prefix, avoiding a
// filename collision between two independently dash-muxed assets.
func prefixMediaPaths(m *mpd.MPD, prefix string) {
	for pi := range m.Periods {
		for ai := range m.Periods[pi].AdaptationSets {
			reps := m.Periods[pi].AdaptationSets[ai].Representations
			for ri := range reps {
				tpl := reps[ri].SegmentTemplate
				if tpl == nil {
					continue
				}
				tpl.Media = prefix + tpl.Media
				tpl.Initialization = prefix + tpl.Initialization
			}
		}
	}
}

// copyDirFiles copies every regular file directly inside src into dst
// (creating dst if needed), non-recursively.
func copyDirFiles(src, dst string) error {
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
