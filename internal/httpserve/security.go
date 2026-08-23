package httpserve

import (
	"io/fs"
	"net/http"
)

// SecurityHeaders wraps next, setting the baseline response headers any
// publicly-reachable HTTP service should send regardless of what it
// serves. Deliberately doesn't set Strict-Transport-Security: TLS
// termination happens in front of this process (a reverse proxy/load
// balancer), not in it, and an app-level HSTS header is wrong to send on
// a connection that arrived as plain HTTP.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// NoDirListing wraps an http.Dir so requests for a directory (no matching
// file) 404 instead of falling through to http.FileServer's default
// directory-listing page.
func NoDirListing(dir http.Dir) http.FileSystem {
	return noListFS{dir}
}

type noListFS struct {
	http.Dir
}

func (nfs noListFS) Open(name string) (http.File, error) {
	f, err := nfs.Dir.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if info.IsDir() {
		_ = f.Close()
		// Reported as fs.ErrNotExist (via *fs.PathError, matching what
		// http.Dir itself returns for a missing file) so net/http's own
		// error translation answers 404, not a 500 that would leak that
		// the path exists but is a directory.
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	return f, nil
}
