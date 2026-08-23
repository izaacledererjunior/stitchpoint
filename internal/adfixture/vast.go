package adfixture

import (
	"encoding/xml"
	"fmt"
	"time"
)

// The types below model just enough of the VAST 4.2 InLine schema to
// produce a spec-conformant response — MediaFiles, TrackingEvents,
// AdVerifications (OMID), and an Extensions block carrying this fixture's
// own "this isn't a real ad decision" disclosure. Fields real responses
// carry but stitchpoint's client (internal/vast) never reads — Icons,
// CompanionAds, Viewable-impression pixels, UniversalAdId, etc. — are
// deliberately omitted: the goal is realistic *shape*, not maximum VAST
// coverage, and internal/vast/vast_test.go already proves the client
// tolerates unknown elements it doesn't model.

type vastDoc struct {
	XMLName xml.Name `xml:"VAST"`
	Version string   `xml:"version,attr"`
	Ad      vastAd   `xml:"Ad"`
}

type vastAd struct {
	ID     string     `xml:"id,attr"`
	InLine vastInline `xml:"InLine"`
}

type vastInline struct {
	AdSystem        string             `xml:"AdSystem"`
	AdTitle         string             `xml:"AdTitle"`
	Description     string             `xml:"Description"`
	Advertiser      string             `xml:"Advertiser"`
	Error           cdata              `xml:"Error"`
	Impression      cdata              `xml:"Impression"`
	Creatives       vastCreatives      `xml:"Creatives"`
	AdVerifications *vastVerifications `xml:"AdVerifications,omitempty"`
	Extensions      vastExtensions     `xml:"Extensions"`
}

type vastCreatives struct {
	Creative vastCreative `xml:"Creative"`
}

type vastCreative struct {
	ID       string     `xml:"id,attr"`
	Sequence int        `xml:"sequence,attr"`
	Linear   vastLinear `xml:"Linear"`
}

type vastLinear struct {
	Duration       string             `xml:"Duration"`
	TrackingEvents vastTrackingEvents `xml:"TrackingEvents"`
	VideoClicks    vastVideoClicks    `xml:"VideoClicks"`
	MediaFiles     vastMediaFiles     `xml:"MediaFiles"`
}

type vastTrackingEvents struct {
	Tracking []vastTracking `xml:"Tracking"`
}

type vastTracking struct {
	Event string `xml:"event,attr"`
	URL   string `xml:",cdata"`
}

type vastVideoClicks struct {
	ClickThrough cdata `xml:"ClickThrough"`
}

type vastMediaFiles struct {
	MediaFile vastMediaFile `xml:"MediaFile"`
}

type vastMediaFile struct {
	ID                  string `xml:"id,attr"`
	Delivery            string `xml:"delivery,attr"`
	Type                string `xml:"type,attr"`
	Width               int    `xml:"width,attr"`
	Height              int    `xml:"height,attr"`
	Bitrate             int    `xml:"bitrate,attr"`
	Scalable            bool   `xml:"scalable,attr"`
	MaintainAspectRatio bool   `xml:"maintainAspectRatio,attr"`
	URL                 string `xml:",cdata"`
}

type vastVerifications struct {
	Verification vastVerification `xml:"Verification"`
}

type vastVerification struct {
	Vendor                 string         `xml:"vendor,attr"`
	JavaScriptResource     vastJSResource `xml:"JavaScriptResource"`
	VerificationParameters cdata          `xml:"VerificationParameters"`
}

type vastJSResource struct {
	APIFramework    string `xml:"apiFramework,attr"`
	BrowserOptional string `xml:"browserOptional,attr"`
	URL             string `xml:",cdata"`
}

type vastExtensions struct {
	Extension vastExtension `xml:"Extension"`
}

type vastExtension struct {
	Type string `xml:"type,attr"`
	Text string `xml:",cdata"`
}

type cdata struct {
	Text string `xml:",cdata"`
}

// fixtureDisclosure is embedded in every response's <Extensions> block —
// the "point X" a real ad-decisioning call would replace this fixture
// with. Kept as a plain string, not just a code comment, so it's visible
// to anyone inspecting the raw VAST response too, not only readers of
// this source file.
const fixtureDisclosure = `This is a static, self-hosted VAST fixture served by stitchpoint's ` +
	`internal/adfixture package for local development, demos, and CI — it is NOT a real ` +
	`ad-decisioning response and always returns the same creative. In a production system, ` +
	`this endpoint (adfixture.Server.handleVAST) is the exact point that would instead call ` +
	`out to a real ad-decision source — an SSP/exchange endpoint, a Prebid Server auction, or ` +
	`Google Ad Manager — and return whichever creative actually won.`

// buildVAST assembles a spec-conformant VAST 4.2 InLine response whose
// every URL is rooted at baseURL, so the response is correct whether
// this server is reached at http://localhost:9090 during local
// development or at its real public domain once deployed.
func buildVAST(baseURL string, cfg Config) vastDoc {
	durationStr := formatVASTDuration(cfg.CreativeDuration)

	return vastDoc{
		Version: "4.2",
		Ad: vastAd{
			ID: "stitchpoint-adfixture",
			InLine: vastInline{
				AdSystem:    "stitchpoint-adfixture",
				AdTitle:     "stitchpoint local VAST fixture",
				Description: fmt.Sprintf("Deterministic local test creative (%s) — not a real ad decision; see AdSystem/Extensions.", creativeFilename(cfg)),
				Advertiser:  "stitchpoint (self-hosted fixture)",
				Error:       cdata{Text: baseURL + "/track?event=error&errorcode=[ERRORCODE]"},
				Impression:  cdata{Text: baseURL + "/track?event=impression"},
				Creatives: vastCreatives{Creative: vastCreative{
					ID:       "1",
					Sequence: 1,
					Linear: vastLinear{
						Duration: durationStr,
						TrackingEvents: vastTrackingEvents{Tracking: []vastTracking{
							{Event: "start", URL: baseURL + "/track?event=start"},
							{Event: "firstQuartile", URL: baseURL + "/track?event=firstQuartile"},
							{Event: "midpoint", URL: baseURL + "/track?event=midpoint"},
							{Event: "thirdQuartile", URL: baseURL + "/track?event=thirdQuartile"},
							{Event: "complete", URL: baseURL + "/track?event=complete"},
						}},
						VideoClicks: vastVideoClicks{ClickThrough: cdata{Text: "https://github.com/izaacledererjunior/stitchpoint"}},
						MediaFiles: vastMediaFiles{MediaFile: vastMediaFile{
							ID:                  "stitchpoint-fixture-mp4",
							Delivery:            "progressive",
							Type:                "video/mp4",
							Width:               cfg.CreativeWidth,
							Height:              cfg.CreativeHeight,
							Bitrate:             cfg.CreativeBitrateKbps,
							Scalable:            true,
							MaintainAspectRatio: true,
							URL:                 baseURL + "/creative.mp4",
						}},
					},
				}},
				AdVerifications: &vastVerifications{Verification: vastVerification{
					Vendor: "stitchpoint-fixture-omid",
					JavaScriptResource: vastJSResource{
						APIFramework:    "omid",
						BrowserOptional: "true",
						URL:             baseURL + "/omid-verification.js",
					},
					VerificationParameters: cdata{Text: "stitchpoint-fixture"},
				}},
				Extensions: vastExtensions{Extension: vastExtension{
					Type: "stitchpoint-fixture",
					Text: fixtureDisclosure,
				}},
			},
		},
	}
}

// formatVASTDuration renders d in VAST's required HH:MM:SS format.
func formatVASTDuration(d time.Duration) string {
	total := int(d.Round(time.Second) / time.Second)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}
