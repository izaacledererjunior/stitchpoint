package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/manifest"
	"github.com/izaacledererjunior/stitchpoint/internal/stitch"
	"github.com/izaacledererjunior/stitchpoint/internal/transcode"
	"github.com/izaacledererjunior/stitchpoint/internal/vast"
)

// runStitch implements the `stitchpoint stitch` subcommand: parse the
// content playlist, resolve the ad (either a pre-encoded local playlist
// via -ad, or a VAST tag via -vast — see resolveAd), splice it into the
// content's signaled break, then materialize the result as a
// self-contained directory — a stitched manifest plus every segment file
// it references — so the output can be handed directly to a player with
// no other setup.
func runStitch(args []string) error {
	fs := flag.NewFlagSet("stitch", flag.ExitOnError)
	contentPath := fs.String("content", "", "path to the content HLS media playlist (.m3u8)")
	adPath := fs.String("ad", "", "path to a pre-encoded ad HLS media playlist (.m3u8)")
	vastURL := fs.String("vast", "", "VAST tag URL to resolve and encode as the ad, instead of -ad")
	outDir := fs.String("out", "", "output directory for the stitched manifest and segment files")
	manifestName := fs.String("manifest-name", "stitched.m3u8", "filename for the stitched manifest inside -out")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *contentPath == "" || *outDir == "" {
		return fmt.Errorf("-content and -out are required")
	}
	if (*adPath == "") == (*vastURL == "") {
		return fmt.Errorf("exactly one of -ad or -vast is required")
	}

	content, contentDir, err := loadPlaylist(*contentPath)
	if err != nil {
		return err
	}

	var ad *manifest.Playlist
	var adDir string
	allowMismatch := false
	if *vastURL != "" {
		ad, adDir, err = resolveAd(*vastURL)
		allowMismatch = true // see stitch.Options.AllowDurationMismatch's doc for why
	} else {
		ad, adDir, err = loadPlaylist(*adPath)
	}
	if err != nil {
		return err
	}

	// Ad segment filenames commonly collide with content segment names
	// (e.g. both "seg_000.ts"), so rename them up front to keep the
	// stitched output directory unambiguous; Splice just copies whatever
	// URIs it's given straight into the result.
	adSourceFile := make(map[string]string, len(ad.Segments)) // renamed URI -> original ad-relative URI
	renamedAd := &manifest.Playlist{Segments: make([]manifest.Segment, len(ad.Segments))}
	for i, s := range ad.Segments {
		newURI := s.URI
		if !strings.HasPrefix(newURI, "ad_") {
			newURI = "ad_" + newURI
		}
		adSourceFile[newURI] = s.URI
		s.URI = newURI
		renamedAd.Segments[i] = s
	}

	out, err := stitch.SpliceWithOptions(content, renamedAd, stitch.Options{AllowDurationMismatch: allowMismatch})
	if err != nil {
		return err
	}

	if err := os.MkdirAll(*outDir, 0o750); err != nil {
		return err
	}

	contentURIs := make(map[string]bool, len(content.Segments))
	for _, s := range content.Segments {
		contentURIs[s.URI] = true
	}

	copied := make(map[string]bool)
	for _, s := range out.Segments {
		if copied[s.URI] {
			continue
		}
		var src string
		switch {
		case contentURIs[s.URI]:
			src = filepath.Join(contentDir, s.URI)
		case adSourceFile[s.URI] != "":
			src = filepath.Join(adDir, adSourceFile[s.URI])
		default:
			return fmt.Errorf("internal error: stitched segment %q traces to neither content nor ad source", s.URI)
		}
		if err := copyFile(src, filepath.Join(*outDir, s.URI)); err != nil {
			return fmt.Errorf("copying segment %q: %w", s.URI, err)
		}
		copied[s.URI] = true
	}

	manifestPath := filepath.Join(*outDir, *manifestName)
	mf, err := os.Create(manifestPath)
	if err != nil {
		return err
	}
	if err := manifest.Write(mf, out); err != nil {
		_ = mf.Close()
		return err
	}
	if err := mf.Close(); err != nil {
		return err
	}

	fmt.Printf("stitched manifest: %s\n", manifestPath)
	fmt.Printf("%d segments total (%d ad segment(s) spliced in)\n", len(out.Segments), len(ad.Segments))
	return nil
}

// resolveAd fetches and follows the given VAST tag URL, downloads the
// selected creative, and encodes it via FFmpeg (internal/transcode) to
// match testdata/vod/content's known encode parameters (see
// transcode.DefaultParams's doc for why those are fixed constants rather
// than probed from -content). Returns the resulting ad playlist and the
// directory its segments were written to.
func resolveAd(vastURL string) (*manifest.Playlist, string, error) {
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

	tmpDir, err := os.MkdirTemp("", "stitchpoint-vast-*")
	if err != nil {
		return nil, "", err
	}

	creativePath := filepath.Join(tmpDir, "creative.mp4")
	if err := transcode.DownloadFile(client, mediaFile.URL, creativePath); err != nil {
		return nil, "", fmt.Errorf("downloading creative: %w", err)
	}

	encodedDir := filepath.Join(tmpDir, "encoded")
	ad, err := transcode.EncodeHLS(creativePath, encodedDir, transcode.DefaultParams)
	if err != nil {
		return nil, "", fmt.Errorf("encoding creative to match content: %w", err)
	}
	return ad, encodedDir, nil
}

func loadPlaylist(path string) (*manifest.Playlist, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = f.Close() }()
	p, err := manifest.Parse(f)
	if err != nil {
		return nil, "", fmt.Errorf("parsing %s: %w", path, err)
	}
	return p, filepath.Dir(path), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	// Close's own error matters here — some filesystems only surface a
	// delayed write/flush failure there, so check it on the success path
	// (the copy-failed path already has a more relevant error to return).
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
