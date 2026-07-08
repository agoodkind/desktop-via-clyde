package daemon

import (
	"strings"
	"testing"
)

func TestParseLoadAverage(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   float64
	}{
		{
			name:   "darwin",
			output: "{ 1.23 0.98 0.76 }",
			want:   1.23,
		},
		{
			name:   "linux",
			output: "2.34 1.11 0.90 1/234 5678",
			want:   2.34,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseLoadAverage([]byte(test.output))
			if err != nil {
				t.Fatalf("parseLoadAverage(%q): %v", test.output, err)
			}
			if got != test.want {
				t.Fatalf("parseLoadAverage(%q) = %v, want %v", test.output, got, test.want)
			}
		})
	}
}

func TestParseLoadAveragePreservesParseError(t *testing.T) {
	_, err := parseLoadAverage([]byte("not-a-number 1.23 0.98"))
	if err == nil {
		t.Fatal("parseLoadAverage succeeded on invalid load output")
	}
	if !strings.Contains(err.Error(), "invalid syntax") {
		t.Fatalf("parseLoadAverage error = %v, want invalid syntax", err)
	}
}
