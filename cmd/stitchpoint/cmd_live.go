package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/httpserve"
	"github.com/izaacledererjunior/stitchpoint/internal/live"
)

// runLive implements the `stitchpoint live` subcommand: start an
// internal/live.Watcher against -upstream and serve its output over HTTP
// until interrupted.
func runLive(args []string) error {
	fs := flag.NewFlagSet("live", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "address to listen on")
	upstream := fs.String("upstream", "", "live HLS media playlist URL to watch; required")
	vastURL := fs.String("vast", "", "VAST tag URL to request when a break starts (for a fully local demo with no real ad network, point this at cmd/vastfixture)")
	pollInterval := fs.Duration("poll-interval", 4*time.Second, "how often to re-fetch the upstream manifest")
	windowSize := fs.Int("window-size", 10, "how many segments the served output window keeps")
	defaultBreakDuration := fs.Duration("default-break-duration", 30*time.Second, "break length to assume when a #EXT-X-CUE-OUT tag carries no duration")
	pprofAddr := fs.String("pprof-addr", "", "if set, serve net/http/pprof on this address (a separate listener from -addr); disabled by default")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *upstream == "" {
		return fmt.Errorf("-upstream is required")
	}
	httpserve.ServePprof(*pprofAddr)

	w, err := live.New(live.Config{
		UpstreamURL:          *upstream,
		DefaultVASTURL:       *vastURL,
		PollInterval:         *pollInterval,
		WindowSize:           *windowSize,
		DefaultBreakDuration: *defaultBreakDuration,
	})
	if err != nil {
		return err
	}
	defer w.Close()

	fmt.Printf("stitchpoint live: listening on %s (upstream=%s)\n", *addr, *upstream)
	fmt.Printf("watch it: curl %s/live/stitched.m3u8\n", "http://localhost"+*addr)
	return httpserve.Serve(*addr, w.Handler(), 10*time.Second)
}
