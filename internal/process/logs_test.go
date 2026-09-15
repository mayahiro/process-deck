package process

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestScanLinesPreservesLineBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name, input string
		want        []string
	}{
		{"empty", "", nil},
		{"empty lines", "\n\n", []string{"", ""}},
		{"CRLF and final line", "one\r\ntwo\r\nthree", []string{"one", "two", "three"}},
		{"final CR", "\r", []string{""}},
		{"embedded CR", "one\rtwo\n", []string{"one\rtwo"}},
		{"exact limit", strings.Repeat("x", MaxLogLineBytes) + "\r\ntail\n", []string{strings.Repeat("x", MaxLogLineBytes), "tail"}},
		{"long final line", strings.Repeat("x", MaxLogLineBytes+1), []string{strings.Repeat("x", MaxLogLineBytes), "x"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			logs := make(chan LogLine, 10)
			if err := scanLines(strings.NewReader(tt.input), "stdout", logs); err != nil {
				t.Fatal(err)
			}
			close(logs)
			var got []string
			for log := range logs {
				got = append(got, log.Line)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("line boundaries differ: got %d records, want %d", len(got), len(tt.want))
			}
		})
	}
}

func TestScanLinesSplitsLongUTF8WithoutLosingOutput(t *testing.T) {
	for _, padding := range []int{0, 1, 2, 3} {
		input := strings.Repeat("x", padding) + strings.Repeat("日本語", MaxLogLineBytes/2)
		logs := make(chan LogLine, 16)
		if err := scanLines(strings.NewReader(input+"\ntail\n"), "stderr", logs); err != nil {
			t.Fatal(err)
		}
		close(logs)
		var chunks []string
		for log := range logs {
			if len(log.Line) > MaxLogLineBytes || !utf8.ValidString(log.Line) || log.Stream != "stderr" {
				t.Fatalf("invalid fragment: size=%d stream=%s", len(log.Line), log.Stream)
			}
			chunks = append(chunks, log.Line)
		}
		if len(chunks) < 3 || chunks[len(chunks)-1] != "tail" || strings.Join(chunks[:len(chunks)-1], "") != input {
			t.Fatal("long line or following output was lost")
		}
	}
}

func TestScanLinesReportsReadFailureAfterPartialOutput(t *testing.T) {
	wantErr := errors.New("read failure")
	logs := make(chan LogLine, 2)
	err := scanLines(io.MultiReader(strings.NewReader("partial"), failingReader{wantErr}), "stdout", logs)
	if !errors.Is(err, wantErr) {
		t.Fatalf("scanLines error = %v", err)
	}
	if len(logs) != 1 || (<-logs).Line != "partial" {
		t.Fatal("partial output missing")
	}
}

type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }
