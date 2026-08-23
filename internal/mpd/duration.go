package mpd

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ParseDuration parses an MPEG-DASH xs:duration string (e.g. "PT120S",
// "PT1H2M3.5S") into a time.Duration. Only hours/minutes/seconds are
// supported, not xs:duration's year/month/day components.
func ParseDuration(s string) (time.Duration, error) {
	orig := s
	if !strings.HasPrefix(s, "P") {
		return 0, fmt.Errorf("mpd: duration %q: missing leading P", orig)
	}
	s = s[1:]

	datePart, timePart, hasTime := strings.Cut(s, "T")
	if datePart != "" {
		return 0, fmt.Errorf("mpd: duration %q: year/month/day components are not supported", orig)
	}
	if !hasTime {
		return 0, nil
	}

	var total time.Duration
	num := strings.Builder{}
	for _, r := range timePart {
		switch {
		case (r >= '0' && r <= '9') || r == '.':
			num.WriteRune(r)
		case r == 'H' || r == 'M' || r == 'S':
			v, err := strconv.ParseFloat(num.String(), 64)
			if err != nil {
				return 0, fmt.Errorf("mpd: duration %q: invalid number before %q: %w", orig, r, err)
			}
			num.Reset()
			switch r {
			case 'H':
				total += time.Duration(v * float64(time.Hour))
			case 'M':
				total += time.Duration(v * float64(time.Minute))
			case 'S':
				total += time.Duration(v * float64(time.Second))
			}
		default:
			return 0, fmt.Errorf("mpd: duration %q: unexpected character %q", orig, r)
		}
	}
	if num.Len() > 0 {
		return 0, fmt.Errorf("mpd: duration %q: trailing digits with no unit", orig)
	}
	return total, nil
}

// FormatDuration renders d as an MPEG-DASH xs:duration string, always in
// "PT<seconds>S" form (valid per the grammar, simpler than H/M/S).
func FormatDuration(d time.Duration) string {
	seconds := d.Seconds()
	// Millisecond precision, trailing zeros dropped ("PT120S", not
	// "PT120.000000000S").
	s := strconv.FormatFloat(seconds, 'f', 3, 64)
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	return "PT" + s + "S"
}
