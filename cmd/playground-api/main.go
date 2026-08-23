// Command playground-api is the HTTP backend for stitchpoint's hosted
// demo — see internal/playground's package doc and
// docs/playground-plan.md. It is deliberately a separate binary from
// stitchpoint (same reasoning as cmd/vastfixture, see ADR 0004): the
// frontend that talks to this API lives in the sibling
// stitchpoint-playground repo, not here.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/httpserve"
	"github.com/izaacledererjunior/stitchpoint/internal/playground"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "playground-api:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("playground-api", flag.ExitOnError)
	addr := fs.String("addr", ":8081", "address to listen on")
	vastURL := fs.String("vast", "", "the only VAST endpoint this server will ever request from; required. Point this at a running cmd/vastfixture instance for the hosted demo — see internal/playground's package doc for why this can't be a per-request parameter")
	demoContent := fs.String("demo-content", "testdata/demo-content/content.m3u8", "already-cued HLS playlist served by POST /api/demo; empty disables demo mode")
	workDir := fs.String("work-dir", "", "base directory for job uploads/output (default: a stitchpoint-playground dir under the OS temp dir)")
	maxUploadMB := fs.Int64("max-upload-mb", 100, "maximum upload size in megabytes")
	maxUploadSeconds := fs.Float64("max-upload-seconds", 90, "maximum uploaded video duration in seconds")
	maxConcurrentJobs := fs.Int("max-concurrent-jobs", 2, "maximum number of jobs running FFmpeg at once; further jobs queue")
	jobTTL := fs.Duration("job-ttl", 30*time.Minute, "how long a job's output is kept before cleanup")
	allowedOrigin := fs.String("allowed-origin", "*", "Access-Control-Allow-Origin sent on every response; the stitchpoint-playground frontend runs on a different origin by design, see internal/playground's package doc")
	maxConcurrentLive := fs.Int("max-concurrent-live", 2, "maximum number of live channels watched at once; further requests are rejected until one stops")
	maxLiveDuration := fs.Duration("max-live-duration", 15*time.Minute, "how long a live session is allowed to run before it's stopped automatically")
	rateLimitPerMinute := fs.Int("rate-limit-per-minute", 20, "maximum job/demo/live-creation requests per client IP per minute; further ones get 429")
	pprofAddr := fs.String("pprof-addr", "", "if set, serve net/http/pprof on this address (a separate listener from -addr, never exposed on the public demo port); disabled by default")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *vastURL == "" {
		return fmt.Errorf("-vast is required")
	}
	httpserve.ServePprof(*pprofAddr)

	srv, err := playground.New(playground.Config{
		VASTURL:            *vastURL,
		DemoContentPath:    *demoContent,
		WorkDir:            *workDir,
		MaxUploadBytes:     *maxUploadMB << 20,
		MaxUploadDuration:  time.Duration(*maxUploadSeconds * float64(time.Second)),
		MaxConcurrentJobs:  *maxConcurrentJobs,
		JobTTL:             *jobTTL,
		AllowedOrigin:      *allowedOrigin,
		MaxConcurrentLive:  *maxConcurrentLive,
		MaxLiveDuration:    *maxLiveDuration,
		RateLimitPerMinute: *rateLimitPerMinute,
	})
	if err != nil {
		return err
	}
	defer srv.Close()

	fmt.Printf("playground-api: listening on %s (vast=%s)\n", *addr, *vastURL)
	if *demoContent != "" {
		fmt.Printf("demo mode: POST /api/demo (content=%s)\n", *demoContent)
	}
	fmt.Printf("try it: curl -i -X POST http://localhost%s/api/demo\n", *addr)
	// ReadHeaderTimeout only bounds header transmission, not the request
	// body, so it doesn't interfere with slow uploads of large videos.
	return httpserve.Serve(*addr, srv.Handler(), 10*time.Second)
}
