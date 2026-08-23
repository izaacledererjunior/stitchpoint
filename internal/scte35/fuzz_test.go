package scte35

import "testing"

// FuzzParse feeds arbitrary bytes to Parse. Parse decodes an external,
// untrusted binary format byte-by-byte via bitReader (see bitreader.go),
// so unlike this package's other tests — which build specific, spec-valid
// or spec-invalid messages by hand — this exists to catch the class of bug
// hand-written cases can't: some byte layout nobody thought to construct
// that trips a panic (index out of range, an unbounded loop) instead of
// the clean error every other malformed-input path returns. A parser that
// can panic on attacker-controlled input is a real risk for any caller
// that decodes SCTE35-* attributes from a third-party manifest.
//
// Seeds are real messages from the table-driven tests above (a
// splice_insert with duration, a component-spliced splice_insert, a
// time_signal, and an unknown command type) plus a couple of the malformed
// cases already known to error cleanly — giving the fuzzer valid
// structure to mutate from, rather than starting purely from noise.
func FuzzParse(f *testing.F) {
	insertCmd := buildSpliceInsert(f, 1001, false, true, false, 27000000, true, 2700000, 42, 1, 1)
	f.Add(buildSection(f, CommandSpliceInsert, insertCmd))

	cancelCmd := buildSpliceInsert(f, 2001, true, false, false, 0, false, 0, 0, 0, 0)
	f.Add(buildSection(f, CommandSpliceInsert, cancelCmd))

	componentsCmd := buildSpliceInsertComponents(3001, true, false, []byte{0x01, 0x02, 0x03}, 9000000, false, 0, 42, 1, 1)
	f.Add(buildSection(f, CommandSpliceInsert, componentsCmd))

	f.Add(buildSection(f, CommandTimeSignal, buildTimeSignal(true, 9000000)))
	f.Add(buildSection(f, CommandTimeSignal, buildTimeSignal(false, 0)))

	f.Add(buildSection(f, CommandSpliceSchedule, []byte{0xAA, 0xBB, 0xCC}))

	f.Add([]byte{})
	f.Add([]byte{expectedTableID})

	f.Fuzz(func(t *testing.T, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Parse(%x) panicked: %v", data, r)
			}
		}()
		_, _ = Parse(data)
	})
}
