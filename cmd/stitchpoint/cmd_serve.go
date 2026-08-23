package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/httpserve"
	"github.com/izaacledererjunior/stitchpoint/internal/server"
)

// runServe implements the `stitchpoint serve` subcommand: start the
// dynamic-SSAI HTTP server (internal/server) and block until it exits.
func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", ":8080", "address to listen on")
	contentPath := fs.String("content", "", "path to the content HLS media playlist (.m3u8); required")
	vastURL := fs.String("vast", "", "default VAST tag URL, used when a request doesn't supply its own ?vast= (for a fully local demo with no real ad network, point this at cmd/vastfixture)")
	sessionTTL := fs.Duration("session-ttl", 30*time.Minute, "how long a session's ad files are kept before cleanup")
	maxConcurrentSessions := fs.Int("max-concurrent-sessions", 4, "maximum number of VAST->download->transcode->splice pipelines running at once; further requests queue")
	rateLimitPerMinute := fs.Int("rate-limit-per-minute", 20, "maximum /vod/manifest requests per client IP per minute; further ones get 429")
	pprofAddr := fs.String("pprof-addr", "", "if set, serve net/http/pprof on this address (a separate listener from -addr — see internal/httpserve.ServePprof for why); disabled by default")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *contentPath == "" {
		return fmt.Errorf("-content is required")
	}
	httpserve.ServePprof(*pprofAddr)

	srv, err := server.New(server.Config{
		ContentPath:           *contentPath,
		DefaultVASTURL:        *vastURL,
		SessionTTL:            *sessionTTL,
		MaxConcurrentSessions: *maxConcurrentSessions,
		RateLimitPerMinute:    *rateLimitPerMinute,
	})
	if err != nil {
		return err
	}
	defer srv.Close()

	fmt.Printf("stitchpoint serve: listening on %s (content=%s)\n", *addr, *contentPath)
	fmt.Printf("start a session: curl -i \"http://localhost%s/vod/manifest?vast=<url>\"\n", *addr)
	return httpserve.Serve(*addr, srv, 10*time.Second)
}
