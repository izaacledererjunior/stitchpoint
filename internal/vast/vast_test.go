package vast

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestFetch_EmptyVASTIsNoFill mirrors a real observed response: Google Ad
// Manager returning a well-formed but empty <VAST/> element (no <Ad> at
// all) when no line item matches the request — a valid "nothing to show"
// outcome, not a malformed response. Callers need to be able to tell this
// apart from a genuine parse/fetch failure via errors.Is(err, ErrNoFill).
func TestFetch_EmptyVASTIsNoFill(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><VAST version="4.0"/>`)
	}))
	defer srv.Close()

	_, err := Fetch(srv.Client(), srv.URL)
	if !errors.Is(err, ErrNoFill) {
		t.Fatalf("Fetch() error = %v, want wrapping ErrNoFill", err)
	}
}

func TestParseBytes_InLineAd(t *testing.T) {
	ad, err := ParseBytes(http.DefaultClient, []byte(inlineFixture))
	if err != nil {
		t.Fatalf("ParseBytes() error = %v", err)
	}
	if ad.AdSystem != "Test Ad System" {
		t.Errorf("AdSystem = %q, want %q", ad.AdSystem, "Test Ad System")
	}
	if _, ok := ad.SelectMediaFile(); !ok {
		t.Fatal("SelectMediaFile() ok = false, want true")
	}
}

func TestParseBytes_FollowsWrapperOverNetwork(t *testing.T) {
	inlineSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, inlineFixture)
	}))
	defer inlineSrv.Close()

	ad, err := ParseBytes(inlineSrv.Client(), []byte(wrapperFixture(inlineSrv.URL)))
	if err != nil {
		t.Fatalf("ParseBytes() error = %v", err)
	}
	if ad.AdSystem != "Test Ad System" {
		t.Errorf("AdSystem = %q, want the InLine ad resolved through the wrapper", ad.AdSystem)
	}
}

func TestParseBytes_EmptyVASTIsNoFill(t *testing.T) {
	_, err := ParseBytes(http.DefaultClient, []byte(`<VAST version="4.0"/>`))
	if !errors.Is(err, ErrNoFill) {
		t.Fatalf("ParseBytes() error = %v, want wrapping ErrNoFill", err)
	}
}

func TestFetch_InLineAd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, inlineFixture)
	}))
	defer srv.Close()

	ad, err := Fetch(srv.Client(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if ad.AdSystem != "Test Ad System" {
		t.Errorf("AdSystem = %q, want %q", ad.AdSystem, "Test Ad System")
	}
	if ad.Duration != 15*time.Second {
		t.Errorf("Duration = %v, want 15s", ad.Duration)
	}
	if len(ad.MediaFiles) != 2 {
		t.Fatalf("got %d MediaFiles, want 2", len(ad.MediaFiles))
	}

	mf, ok := ad.SelectMediaFile()
	if !ok {
		t.Fatal("SelectMediaFile() ok = false, want true")
	}
	if mf.Bitrate != 2000 {
		t.Errorf("selected MediaFile.Bitrate = %d, want 2000 (highest progressive mp4)", mf.Bitrate)
	}
	if mf.URL != "https://example.com/creative-hi.mp4" {
		t.Errorf("selected MediaFile.URL = %q, want the high-bitrate mp4", mf.URL)
	}
}

func TestFetch_FollowsWrapperChain(t *testing.T) {
	var inlineURL string
	wrapper2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, wrapperFixture(inlineURL))
	}))
	defer wrapper2.Close()

	wrapper1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, wrapperFixture(wrapper2.URL))
	}))
	defer wrapper1.Close()

	inlineSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, inlineFixture)
	}))
	defer inlineSrv.Close()
	inlineURL = inlineSrv.URL

	ad, err := Fetch(wrapper1.Client(), wrapper1.URL)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if ad.AdSystem != "Test Ad System" {
		t.Errorf("AdSystem = %q, want the InLine ad resolved through two wrappers", ad.AdSystem)
	}
}

func TestFetch_WrapperLoopHitsDepthLimit(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, wrapperFixture(srvURL)) // points at itself
	}))
	defer srv.Close()
	srvURL = srv.URL

	_, err := Fetch(srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("Fetch() error = nil, want depth-limit error on a wrapper loop")
	}
}

func TestFetch_NoMediaFilesIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `<VAST version="3.0"><Ad id="1"><InLine>
			<AdSystem>Test</AdSystem><AdTitle>No Media</AdTitle>
			<Creatives><Creative><Linear><Duration>00:00:15</Duration>
				<MediaFiles></MediaFiles>
			</Linear></Creative></Creatives>
		</InLine></Ad></VAST>`)
	}))
	defer srv.Close()

	_, err := Fetch(srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("Fetch() error = nil, want error for an ad with no MediaFiles")
	}
}

func TestFetch_MalformedXMLIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `not xml at all`)
	}))
	defer srv.Close()

	_, err := Fetch(srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("Fetch() error = nil, want error for malformed XML")
	}
}

func TestFetch_HTTPErrorStatusIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := Fetch(srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("Fetch() error = nil, want error for a non-200 response")
	}
}

func TestSelectMediaFile_SkipsStreamingDelivery(t *testing.T) {
	ad := ResolvedAd{MediaFiles: []MediaFile{
		{Delivery: "streaming", Type: "application/x-mpegURL", URL: "https://example.com/hls/master.m3u8"},
	}}
	if _, ok := ad.SelectMediaFile(); ok {
		t.Fatal("SelectMediaFile() ok = true, want false (only a streaming-delivery file offered)")
	}
}

const inlineFixture = `<?xml version="1.0" encoding="UTF-8"?>
<VAST version="3.0">
  <Ad id="123">
    <InLine>
      <AdSystem>Test Ad System</AdSystem>
      <AdTitle>Test Ad</AdTitle>
      <Creatives>
        <Creative>
          <Linear>
            <Duration>00:00:15</Duration>
            <MediaFiles>
              <MediaFile delivery="progressive" type="video/mp4" width="1280" height="720" bitrate="2000"><![CDATA[https://example.com/creative-hi.mp4]]></MediaFile>
              <MediaFile delivery="progressive" type="video/mp4" width="640" height="360" bitrate="800"><![CDATA[https://example.com/creative-lo.mp4]]></MediaFile>
            </MediaFiles>
          </Linear>
        </Creative>
      </Creatives>
    </InLine>
  </Ad>
</VAST>`

func wrapperFixture(nextURL string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<VAST version="3.0">
  <Ad id="1">
    <Wrapper>
      <AdSystem>Wrapper Ad System</AdSystem>
      <VASTAdTagURI><![CDATA[` + nextURL + `]]></VASTAdTagURI>
    </Wrapper>
  </Ad>
</VAST>`
}
