// Package response wraps CLI-visible text and JSON with correlation metadata.
package response

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"

	"goodkind.io/gklog/correlation"
)

type textHeaderGuard struct {
	mu      sync.Mutex
	written bool
}

type textHeaderGuardKey struct{}

// WithTextHeaderGuard returns a context carrying a one-shot text header guard so
// that WriteTextHeaderOnce emits the correlation header at most once per command
// execution, regardless of how many components try to write it.
func WithTextHeaderGuard(ctx context.Context) context.Context {
	return context.WithValue(ctx, textHeaderGuardKey{}, &textHeaderGuard{mu: sync.Mutex{}, written: false})
}

// WriteTextHeaderOnce writes the text metadata header line for ctx, but only the
// first time it is called for a context carrying a guard from WithTextHeaderGuard.
// It is the single component responsible for emitting the text trace header, so
// callers never need to prepend the header themselves.
func WriteTextHeaderOnce(ctx context.Context, out io.Writer) error {
	header := FromContext(ctx).HeaderLine()
	if header == "" {
		return nil
	}
	if guard, ok := ctx.Value(textHeaderGuardKey{}).(*textHeaderGuard); ok {
		guard.mu.Lock()
		defer guard.mu.Unlock()
		if guard.written {
			return nil
		}
		guard.written = true
	}
	if _, err := io.WriteString(out, header+"\n"); err != nil {
		slog.WarnContext(ctx, "response.write_text_header_failed", "err", err)
		return fmt.Errorf("response: write text header: %w", err)
	}
	return nil
}

// Metadata is the user-visible subset of the current correlation context.
type Metadata struct {
	RequestID    string `json:"request_id,omitempty"`
	TraceID      string `json:"trace_id,omitempty"`
	SpanID       string `json:"span_id,omitempty"`
	ParentSpanID string `json:"parent_span_id,omitempty"`
}

// JSONStyle controls JSON formatting.
type JSONStyle uint8

const (
	// JSONCompact writes one compact JSON document.
	JSONCompact JSONStyle = iota
	// JSONIndented writes one indented JSON document.
	JSONIndented
)

// JSONDocument is the top-level response envelope for scalar payloads.
type JSONDocument struct {
	Meta   Metadata        `json:"_meta"`
	Result json.RawMessage `json:"result"`
}

// FromContext projects correlation metadata from ctx.
func FromContext(ctx context.Context) Metadata {
	corr := correlation.FromContext(ctx)
	return Metadata{
		RequestID:    strings.TrimSpace(corr.RequestID),
		TraceID:      string(corr.TraceID),
		SpanID:       string(corr.SpanID),
		ParentSpanID: string(corr.ParentSpanID),
	}
}

// HeaderLine renders one text header line for the current metadata.
func (metadata Metadata) HeaderLine() string {
	fields := make([]string, 0, 4)
	if metadata.TraceID != "" {
		fields = append(fields, "trace_id="+metadata.TraceID)
	}
	if metadata.SpanID != "" {
		fields = append(fields, "span_id="+metadata.SpanID)
	}
	if metadata.ParentSpanID != "" {
		fields = append(fields, "parent_span_id="+metadata.ParentSpanID)
	}
	if metadata.RequestID != "" {
		fields = append(fields, "request_id="+metadata.RequestID)
	}
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}

// JSON wraps payload with _meta metadata.
func JSON(ctx context.Context, payload []byte, style JSONStyle) ([]byte, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("response: empty json payload")
	}
	if !json.Valid(trimmed) {
		return nil, fmt.Errorf("response: invalid json payload")
	}

	objectPayload := make(map[string]json.RawMessage)
	if err := json.Unmarshal(trimmed, &objectPayload); err == nil {
		metaBody, err := json.Marshal(FromContext(ctx))
		if err != nil {
			slog.WarnContext(ctx, "response.encode_json_meta_failed", "err", err)
			return nil, fmt.Errorf("response: encode json metadata: %w", err)
		}
		objectPayload["_meta"] = metaBody
		return marshalJSONObject(ctx, objectPayload, style)
	}

	document := JSONDocument{
		Meta:   FromContext(ctx),
		Result: append(json.RawMessage(nil), trimmed...),
	}
	return marshalJSONDocument(ctx, document, style)
}

// WriteJSON writes one JSON response document.
func WriteJSON(ctx context.Context, out io.Writer, payload []byte, style JSONStyle) error {
	body, err := JSON(ctx, payload, style)
	if err != nil {
		return err
	}
	if _, err := out.Write(body); err != nil {
		slog.WarnContext(ctx, "response.write_json_failed", "err", err)
		return fmt.Errorf("response: write json: %w", err)
	}
	return nil
}

func marshalJSONObject(ctx context.Context, payload map[string]json.RawMessage, style JSONStyle) ([]byte, error) {
	var (
		body []byte
		err  error
	)
	switch style {
	case JSONCompact:
		body, err = json.Marshal(payload)
	case JSONIndented:
		body, err = json.MarshalIndent(payload, "", "  ")
	}
	if err != nil {
		slog.WarnContext(ctx, "response.encode_json_failed", "err", err)
		return nil, fmt.Errorf("response: encode json object: %w", err)
	}
	return append(body, '\n'), nil
}

func marshalJSONDocument(ctx context.Context, payload JSONDocument, style JSONStyle) ([]byte, error) {
	var (
		body []byte
		err  error
	)
	switch style {
	case JSONCompact:
		body, err = json.Marshal(payload)
	case JSONIndented:
		body, err = json.MarshalIndent(payload, "", "  ")
	}
	if err != nil {
		slog.WarnContext(ctx, "response.encode_json_failed", "err", err)
		return nil, fmt.Errorf("response: encode json document: %w", err)
	}
	return append(body, '\n'), nil
}
