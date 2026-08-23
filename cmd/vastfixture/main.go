// Command vastfixture runs a small, deterministic VAST ad server for
// local development, demos, and CI — never a real ad-decisioning system.
// See internal/adfixture's package doc for exactly why this exists and
// where a real ad-decision call would replace it.
//
// It is a separate binary from stitchpoint on purpose: it's a different
// role (an ad server, not an SSAI stitcher) and, per the project's plan
// to eventually deploy a hosted playground, a separate binary is a
// separate, independently deployable service.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/izaacledererjunior/stitchpoint/internal/adfixture"
	"github.com/izaacledererjunior/stitchpoint/internal/httpserve"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "vastfixture:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("vastfixture", flag.ExitOnError)
	addr := fs.String("addr", ":9090", "address to listen on")
	creative := fs.String("creative", "testdata/demo-ad/advertising.mp4", "path to the progressive MP4 creative to serve — see adfixture.DefaultConfig for the dimensions/bitrate/duration reported alongside it (only correct for the default creative; pass mismatched width/height/duration via a custom adfixture.Config if you swap in a different file)")
	baseURL := fs.String("base-url", "", "scheme+host to use in served URLs (e.g. https://vastfixture.example.com); leave unset to derive it from each request's Host header, which is correct for most local and cloud deployments")
	rateLimitPerMinute := fs.Int("rate-limit-per-minute", 20, "maximum /vast requests per client IP per minute; further ones get 429")
	pprofAddr := fs.String("pprof-addr", "", "if set, serve net/http/pprof on this address (a separate listener from -addr); disabled by default")
	if err := fs.Parse(args); err != nil {
		return err
	}
	httpserve.ServePprof(*pprofAddr)

	srv, err := adfixture.New(adfixture.Config{
		CreativePath:       *creative,
		BaseURL:            *baseURL,
		RateLimitPerMinute: *rateLimitPerMinute,
	})
	if err != nil {
		return err
	}
	defer srv.Close()

	fmt.Printf("vastfixture: listening on %s (creative=%s)\n", *addr, *creative)
	fmt.Printf("point stitchpoint at it: -vast \"http://localhost%s/vast\"\n", *addr)
	fmt.Printf("try it yourself: curl http://localhost%s/vast\n", *addr)
	return httpserve.Serve(*addr, srv.Handler(), 10*time.Second)
}
