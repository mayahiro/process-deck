package process

import (
	"bufio"
	"errors"
	"io"
	"time"
	"unicode/utf8"
)

// MaxLogLineBytes bounds each captured log record. Longer logical lines are
// split into consecutive records without breaking valid UTF-8 characters.
const MaxLogLineBytes = 1024 * 1024

func scanLines(r io.Reader, stream string, logs chan<- LogLine) error {
	reader := bufio.NewReaderSize(r, 64*1024)
	pending := make([]byte, 0, 64*1024)
	emit := func(line []byte) {
		logs <- LogLine{Stream: stream, Line: string(line), Time: time.Now()}
	}
	for {
		part, err := reader.ReadSlice('\n')
		endOfLine := len(part) > 0 && part[len(part)-1] == '\n'
		if endOfLine {
			part = part[:len(part)-1]
		}
		pending = append(pending, part...)
		hasContent := len(pending) > 0
		endOfRead := err != nil && !errors.Is(err, bufio.ErrBufferFull)
		if (endOfLine || endOfRead) && len(pending) > 0 && pending[len(pending)-1] == '\r' {
			pending = pending[:len(pending)-1]
		}
		for len(pending) > MaxLogLineBytes {
			cut := MaxLogLineBytes
			// Move a boundary inside a valid multibyte rune to its beginning.
			start := cut
			for start > cut-utf8.UTFMax && !utf8.RuneStart(pending[start]) {
				start--
			}
			if start < cut {
				_, size := utf8.DecodeRune(pending[start:])
				if start+size > cut || !utf8.FullRune(pending[start:]) {
					cut = start
				}
			}
			emit(pending[:cut])
			pending = pending[:copy(pending, pending[cut:])]
		}
		if endOfLine || (endOfRead && hasContent) {
			emit(pending)
			pending = pending[:0]
		}
		if endOfRead {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}
