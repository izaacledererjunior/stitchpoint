package httpserve

import (
	"net/http"
	"path/filepath"
)

// mediaContentType maps a served file's extension to its correct HLS
// media type. Go's stdlib (http.ServeFile, http.FileServer) falls back to
// mime.TypeByExtension when no Content-Type is set, which reads the
// host OS's own mime database — and on at least one real deployment
// environment, ".ts" (MPEG Transport Stream, the HLS segment format)
// collides with Qt Linguist's own ".ts" translation-source extension,
// getting served as text/vnd.trolltech.linguist instead of video/mp2t.
// That corrupted live ad segment playback via MSE-based players in a
// real production incident — confirmed by curling a stuck session's own
// segment and finding the wrong Content-Type header, with the underlying
// bytes themselves decoding perfectly fine via ffmpeg. Serving these
// extensions explicitly removes the dependency on the host's mime
// database entirely, rather than hoping every deploy target's happens to
// map them correctly.
func mediaContentType(path string) string {
	switch filepath.Ext(path) {
	case ".ts":
		return "video/mp2t"
	case ".m3u8":
		return "application/vnd.apple.mpegurl"
	case ".mp4":
		return "video/mp4"
	default:
		return ""
	}
}

// ServeMediaFile is http.ServeFile with an explicit, extension-based
// Content-Type for known HLS media types (see mediaContentType) set
// first — ServeFile only falls back to the OS mime database when the
// header isn't already present, so setting it here always wins.
func ServeMediaFile(w http.ResponseWriter, r *http.Request, path string) {
	if ct := mediaContentType(path); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeFile(w, r, path)
}

// WithMediaContentType wraps next (typically http.FileServer), presetting
// Content-Type by the request path's extension for known HLS media types
// before delegating — see mediaContentType and ServeMediaFile's doc for
// why the OS mime database can't be trusted for this.
func WithMediaContentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := mediaContentType(r.URL.Path); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		next.ServeHTTP(w, r)
	})
}
