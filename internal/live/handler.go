package live

import (
	"net/http"

	"github.com/izaacledererjunior/stitchpoint/internal/httpserve"
	"github.com/izaacledererjunior/stitchpoint/internal/manifest"
)

// Handler returns an http.Handler serving the Watcher's live output:
//
//	GET /live/stitched.m3u8   the current stitched manifest window
//	GET /live/ads/{gen}/{file}  an ad segment from a specific break
//
// Kept separate from internal/server: a live channel is one shared stream
// for every viewer, not a per-request session.
func (w *Watcher) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /live/stitched.m3u8", w.handleManifest)
	mux.HandleFunc("GET /live/ads/{gen}/{file}", w.handleAdSegment)
	return httpserve.SecurityHeaders(mux)
}

func (w *Watcher) handleManifest(rw http.ResponseWriter, _ *http.Request) {
	rw.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	rw.Header().Set("Cache-Control", "no-cache")
	if err := manifest.Write(rw, w.CurrentManifest()); err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
	}
}

func (w *Watcher) handleAdSegment(rw http.ResponseWriter, r *http.Request) {
	gen := r.PathValue("gen")
	file := r.PathValue("file")
	path, ok := w.AdSegmentPath(gen, file)
	if !ok {
		http.NotFound(rw, r)
		return
	}
	http.ServeFile(rw, r, path)
}
