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

	"goodkind.io/gklog/correlation"
)

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

// Text prepends the metadata header when one exists.
func Text(ctx context.Context, body string) string {
	header := FromContext(ctx).HeaderLine()
	if header == "" {
		return body
	}
	if body == "" {
		return header + "\n"
	}
	return header + "\n" + body
}

// WriteText writes one text response.
func WriteText(ctx context.Context, out io.Writer, body string) error {
	if _, err := io.WriteString(out, Text(ctx, body)); err != nil {
		slog.WarnContext(ctx, "response.write_text_failed", "err", err)
		return fmt.Errorf("response: write text: %w", err)
	}
	return nil
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
