package main

import (
	"bufio"
	"io"
	"strings"
)

// readLines returns every non-blank, trimmed line from r. Shared by any
// subcommand that accepts newline-delimited input (currently just scte35's
// -file/stdin modes).
func readLines(r io.Reader) ([]string, error) {
	var lines []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines, sc.Err()
}
