// Package ssrf is a basic SSRF (server-side request forgery) mitigation
// for the handful of places in this project that fetch a caller-supplied
// URL by design (a live channel's -upstream, the VOD dynamic server's
// per-request ?vast=). It resolves the hostname once and rejects
// loopback/private/link-local/unspecified addresses — stops the careless
// cases (localhost, 169.254.169.254, RFC1918 ranges), not a determined
// DNS-rebinding attack (resolve public for this check, then private for
// the real fetch); closing that gap needs re-validating on every use, not
// just once here, and is deferred.
package ssrf

import (
	"fmt"
	"net"
	"net/url"
)

// ValidatePublicHTTPURL returns an error if rawURL isn't a plain http(s)
// URL whose host resolves only to public addresses.
func ValidatePublicHTTPURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL has no host")
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("resolving host: %w", err)
	}
	for _, ip := range ips {
		if isDisallowedIP(ip) {
			return fmt.Errorf("host %q resolves to a non-public address (%s), not allowed", host, ip)
		}
	}
	return nil
}

func isDisallowedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
