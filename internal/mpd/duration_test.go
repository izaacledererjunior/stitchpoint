package mpd

import (
	"testing"
	"time"
)

func TestParseDuration(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"seconds only", "PT120S", 120 * time.Second, false},
		{"fractional seconds", "PT9.450S", 9450 * time.Millisecond, false},
		{"hours minutes seconds", "PT1H2M3S", time.Hour + 2*time.Minute + 3*time.Second, false},
		{"zero", "PT0S", 0, false},
		{"missing leading P", "T120S", 0, true},
		{"date component unsupported", "P1DT2S", 0, true},
		{"garbage unit", "PT5X", 0, true},
		{"trailing digits no unit", "PT5", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDuration(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseDuration(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("ParseDuration(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want string
	}{
		{"whole seconds", 120 * time.Second, "PT120S"},
		{"fractional", 9450 * time.Millisecond, "PT9.45S"},
		{"zero", 0, "PT0S"},
		{"sub-millisecond rounds away", 119987 * time.Millisecond, "PT119.987S"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatDuration(tt.in); got != tt.want {
				t.Errorf("FormatDuration(%v) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDuration_RoundTrip(t *testing.T) {
	for _, d := range []time.Duration{0, 120 * time.Second, 9450 * time.Millisecond, 119987 * time.Millisecond} {
		s := FormatDuration(d)
		got, err := ParseDuration(s)
		if err != nil {
			t.Fatalf("ParseDuration(FormatDuration(%v)=%q) error = %v", d, s, err)
		}
		if got != d {
			t.Errorf("round-trip %v -> %q -> %v, want %v", d, s, got, d)
		}
	}
}
