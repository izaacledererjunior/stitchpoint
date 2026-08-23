package scte35

import (
	"strings"
	"testing"
)

func TestDescribe(t *testing.T) {
	tests := []struct {
		name    string
		section *SpliceInfoSection
		want    []string // substrings that must appear
	}{
		{
			name: "cue-out with duration",
			section: &SpliceInfoSection{
				SpliceCommand: SpliceInsert{
					SpliceEventID:         1,
					OutOfNetworkIndicator: true,
					DurationFlag:          true,
					BreakDuration:         &BreakDuration{Duration: 30 * PTSTicksPerSecond},
				},
			},
			want: []string{"CUE-OUT", "event=1", "30s"},
		},
		{
			name: "cancelled event",
			section: &SpliceInfoSection{
				SpliceCommand: SpliceInsert{SpliceEventID: 5, SpliceEventCancelIndicator: true},
			},
			want: []string{"CANCEL", "event=5"},
		},
		{
			name: "time_signal with pts",
			section: &SpliceInfoSection{
				SpliceCommand: TimeSignal{SpliceTime: SpliceTime{TimeSpecifiedFlag: true, PTSTime: 90 * PTSTicksPerSecond}},
			},
			want: []string{"time_signal", "pts=1m30s"},
		},
		{
			name:    "raw/undecoded command",
			section: &SpliceInfoSection{SpliceCommand: RawCommand{Type: CommandSpliceSchedule, Payload: []byte{1, 2, 3}}},
			want:    []string{"0x04", "3 bytes"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Describe(tc.section)
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("Describe() = %q, want substring %q", got, want)
				}
			}
		})
	}
}
