package batchops

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

type linePrefixWriter struct {
	out         io.Writer
	mu          *sync.Mutex
	prefix      string
	startOfLine bool
}

func newLinePrefixWriter(out io.Writer, mu *sync.Mutex, targetID string) *linePrefixWriter {
	return &linePrefixWriter{
		out:         out,
		mu:          mu,
		prefix:      fmt.Sprintf("[%s] ", targetID),
		startOfLine: true,
	}
}

func (w *linePrefixWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	written := 0
	remaining := data
	for len(remaining) > 0 {
		if w.startOfLine {
			if _, err := io.WriteString(w.out, w.prefix); err != nil {
				return written, fmt.Errorf("write line prefix: %w", err)
			}
			w.startOfLine = false
		}
		newlineIndex := bytes.IndexByte(remaining, '\n')
		if newlineIndex == -1 {
			n, err := w.out.Write(remaining)
			written += n
			if err != nil {
				return written, fmt.Errorf("write line chunk: %w", err)
			}
			return written, nil
		}
		chunk := remaining[:newlineIndex+1]
		n, err := w.out.Write(chunk)
		written += n
		if err != nil {
			return written, fmt.Errorf("write line chunk: %w", err)
		}
		w.startOfLine = true
		remaining = remaining[newlineIndex+1:]
	}
	return written, nil
}

func writeBatchLine(out io.Writer, mu *sync.Mutex, line string) error {
	mu.Lock()
	defer mu.Unlock()
	if _, err := fmt.Fprintf(out, "%s\n", line); err != nil {
		slog.Warn("batchops.write_line_failed", "err", err)
		return fmt.Errorf("write batch line: %w", err)
	}
	return nil
}
