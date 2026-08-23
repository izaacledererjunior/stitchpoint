package ssrf

import "testing"

func TestValidatePublicHTTPURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "loopback", url: "http://127.0.0.1/vast.xml", wantErr: true},
		{name: "loopback hostname", url: "http://localhost/vast.xml", wantErr: true},
		{name: "IPv6 loopback", url: "http://[::1]/vast.xml", wantErr: true},
		{name: "private RFC1918", url: "http://10.0.0.5/vast.xml", wantErr: true},
		{name: "link-local (cloud metadata)", url: "http://169.254.169.254/latest/meta-data/", wantErr: true},
		{name: "unspecified", url: "http://0.0.0.0/vast.xml", wantErr: true},
		{name: "no scheme", url: "example.com/vast.xml", wantErr: true},
		{name: "non-http scheme", url: "file:///etc/passwd", wantErr: true},
		{name: "malformed URL", url: "http://[::not-valid", wantErr: true},
		{name: "public address", url: "http://93.184.216.34/vast.xml", wantErr: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePublicHTTPURL(tc.url)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidatePublicHTTPURL(%q) error = %v, wantErr %v", tc.url, err, tc.wantErr)
			}
		})
	}
}
