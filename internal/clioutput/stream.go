package clioutput

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"goodkind.io/desktop-via-clyde/internal/response"
)

// ProgressEvent is one structured progress record.
type ProgressEvent struct {
	Type    string `json:"type"`
	Scope   string `json:"scope,omitempty"`
	Target  string `json:"target,omitempty"`
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
}

type progressDocument struct {
	Meta response.Metadata `json:"_meta"`
	ProgressEvent
}

// JSONLineWriter converts newline-delimited progress text into JSON events.
type JSONLineWriter struct {
	metadata response.Metadata
	out      io.Writer
	scope    string
	target   string

	mu      sync.Mutex
	pending []byte
}

// NewJSONLineWriter builds a JSON event writer for one progress stream.
func NewJSONLineWriter(ctx context.Context, out io.Writer, scope string, target string) *JSONLineWriter {
	return &JSONLineWriter{
		metadata: response.FromContext(ctx),
		out:      out,
		scope:    scope,
		target:   target,
		mu:       sync.Mutex{},
		pending:  nil,
	}
}

// Write converts complete lines into JSON events.
func (w *JSONLineWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.pending = append(w.pending, data...)
	for {
		newlineIndex := bytes.IndexByte(w.pending, '\n')
		if newlineIndex == -1 {
			return len(data), nil
		}
		line := strings.TrimSpace(string(w.pending[:newlineIndex]))
		w.pending = append([]byte(nil), w.pending[newlineIndex+1:]...)
		if line == "" {
			continue
		}
		if err := writeProgressDocument(w.out, progressDocument{
			Meta: w.metadata,
			ProgressEvent: ProgressEvent{
				Type:    "progress",
				Scope:   w.scope,
				Target:  w.target,
				Status:  "",
				Message: line,
			},
		}); err != nil {
			return 0, err
		}
	}
}

func writeProgressDocument(out io.Writer, payload progressDocument) error {
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("clioutput.progress.marshal_failed", "err", err)
		return fmt.Errorf("marshal json output payload: %w", err)
	}
	if _, err := out.Write(append(body, '\n')); err != nil {
		slog.Warn("clioutput.progress.write_failed", "err", err)
		return fmt.Errorf("write json output payload: %w", err)
	}
	return nil
}
