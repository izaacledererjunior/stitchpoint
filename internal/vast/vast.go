// Package vast is a minimal VAST (Video Ad Serving Template) client: fetch
// a VAST tag URL, follow Wrapper redirects, and return the InLine ad's
// media files. A client, not a server — stitchpoint consumes an
// ad-decision response, it doesn't implement ad-decisioning. Scope is
// narrow: AdSystem/AdTitle, Linear/Duration, MediaFiles only — tracking
// pixels are never parsed or fired (out of scope, see README "Non-goals").
package vast

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrNoFill is returned when a VAST endpoint responds successfully but
// with no <Ad> at all — a valid "nothing to show" response, not a
// malformed response or a bug in this client.
var ErrNoFill = errors.New("vast: no ad returned (no-fill)")

// MaxWrapperDepth bounds chained VASTAdTagURI redirects, a safety bound
// against a misconfigured or malicious loop.
const MaxWrapperDepth = 5

// MediaFile is one playable creative file offered by an InLine ad.
type MediaFile struct {
	Delivery string // "progressive" or "streaming"
	Type     string // MIME type, e.g. "video/mp4"
	Width    int
	Height   int
	Bitrate  int // kbps, as reported by the tag; 0 if not provided
	URL      string
}

// ResolvedAd is the result of following a VAST tag (and any Wrapper
// chain) to a final InLine ad.
type ResolvedAd struct {
	AdSystem   string
	AdTitle    string
	Duration   time.Duration // from Linear/Duration
	MediaFiles []MediaFile
}

// SelectMediaFile picks the best progressive-download MP4 by bitrate —
// what internal/transcode expects as input. Streaming-delivery files
// (HLS/DASH manifests) are skipped. Returns false if nothing suitable.
func (a ResolvedAd) SelectMediaFile() (MediaFile, bool) {
	var best MediaFile
	found := false
	for _, mf := range a.MediaFiles {
		if mf.Delivery != "progressive" || !strings.EqualFold(mf.Type, "video/mp4") {
			continue
		}
		if !found || mf.Bitrate > best.Bitrate {
			best = mf
			found = true
		}
	}
	return best, found
}

// Fetch requests a VAST tag URL and follows any Wrapper chain to the
// final InLine ad. client is the HTTP client to use (callers should set a
// timeout — VAST endpoints, especially programmatic ones, can be slow).
func Fetch(client *http.Client, url string) (*ResolvedAd, error) {
	return fetchDepth(client, url, 0)
}

// ParseBytes resolves an already-fetched VAST document. Any Wrapper is
// still followed over the network via client; only the first request is
// local.
func ParseBytes(client *http.Client, data []byte) (*ResolvedAd, error) {
	doc, err := parse(data)
	if err != nil {
		return nil, fmt.Errorf("vast: parsing local document: %w", err)
	}
	return resolveDoc(client, doc, "local document", 0)
}

func fetchDepth(client *http.Client, url string, depth int) (*ResolvedAd, error) {
	if depth >= MaxWrapperDepth {
		return nil, fmt.Errorf("vast: exceeded max wrapper redirect depth (%d) at %s", MaxWrapperDepth, url)
	}

	body, err := get(client, url)
	if err != nil {
		return nil, fmt.Errorf("vast: fetching %s: %w", url, err)
	}

	doc, err := parse(body)
	if err != nil {
		return nil, fmt.Errorf("vast: parsing response from %s: %w", url, err)
	}

	return resolveDoc(client, doc, url, depth)
}

// resolveDoc handles a parsed VAST document's first <Ad>: following a
// Wrapper over the network (bounded by depth), or resolving an InLine ad
// directly. source is used only for error messages.
func resolveDoc(client *http.Client, doc *vastXML, source string, depth int) (*ResolvedAd, error) {
	if len(doc.Ads) == 0 {
		return nil, fmt.Errorf("%w (from %s — check targeting/geo/inventory, or that requests from this network are expected to match a campaign)", ErrNoFill, source)
	}
	ad := doc.Ads[0]

	if ad.Wrapper != nil {
		next := strings.TrimSpace(ad.Wrapper.VASTAdTagURI)
		if next == "" {
			return nil, fmt.Errorf("vast: Wrapper at %s has no VASTAdTagURI", source)
		}
		return fetchDepth(client, next, depth+1)
	}

	if ad.InLine == nil {
		return nil, fmt.Errorf("vast: <Ad> at %s has neither InLine nor Wrapper", source)
	}
	return resolveInLine(ad.InLine)
}

func resolveInLine(in *inLineXML) (*ResolvedAd, error) {
	resolved := &ResolvedAd{AdSystem: in.AdSystem, AdTitle: in.AdTitle}

	for _, creative := range in.Creatives {
		if creative.Linear == nil {
			continue
		}
		if d, err := parseVASTDuration(creative.Linear.Duration); err == nil {
			resolved.Duration = d
		}
		for _, mf := range creative.Linear.MediaFiles {
			resolved.MediaFiles = append(resolved.MediaFiles, MediaFile{
				Delivery: mf.Delivery,
				Type:     mf.Type,
				Width:    mf.Width,
				Height:   mf.Height,
				Bitrate:  mf.Bitrate,
				URL:      strings.TrimSpace(mf.URL),
			})
		}
	}

	if len(resolved.MediaFiles) == 0 {
		return nil, fmt.Errorf("vast: InLine ad %q has no MediaFiles", in.AdTitle)
	}
	return resolved, nil
}

// parseVASTDuration parses VAST's Linear/Duration format, HH:MM:SS or
// HH:MM:SS.mmm.
func parseVASTDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0, fmt.Errorf("vast: invalid Duration %q", s)
	}
	hours, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("vast: invalid Duration %q: %w", s, err)
	}
	minutes, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("vast: invalid Duration %q: %w", s, err)
	}
	seconds, err := strconv.ParseFloat(parts[2], 64)
	if err != nil {
		return 0, fmt.Errorf("vast: invalid Duration %q: %w", s, err)
	}
	total := time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute + time.Duration(seconds*float64(time.Second))
	return total, nil
}

func get(client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// --- XML schema (unexported: only the fields stitchpoint needs) ---

type vastXML struct {
	XMLName xml.Name `xml:"VAST"`
	Ads     []adXML  `xml:"Ad"`
}

type adXML struct {
	ID      string      `xml:"id,attr"`
	InLine  *inLineXML  `xml:"InLine"`
	Wrapper *wrapperXML `xml:"Wrapper"`
}

type inLineXML struct {
	AdSystem  string        `xml:"AdSystem"`
	AdTitle   string        `xml:"AdTitle"`
	Creatives []creativeXML `xml:"Creatives>Creative"`
}

type wrapperXML struct {
	AdSystem     string `xml:"AdSystem"`
	VASTAdTagURI string `xml:"VASTAdTagURI"`
}

type creativeXML struct {
	Linear *linearXML `xml:"Linear"`
}

type linearXML struct {
	Duration   string         `xml:"Duration"`
	MediaFiles []mediaFileXML `xml:"MediaFiles>MediaFile"`
}

type mediaFileXML struct {
	Delivery string `xml:"delivery,attr"`
	Type     string `xml:"type,attr"`
	Width    int    `xml:"width,attr"`
	Height   int    `xml:"height,attr"`
	Bitrate  int    `xml:"bitrate,attr"`
	URL      string `xml:",chardata"`
}

func parse(data []byte) (*vastXML, error) {
	var doc vastXML
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}
