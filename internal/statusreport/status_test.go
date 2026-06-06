package statusreport

import "testing"

func TestUpstreamSigningLabelExtractsTeamFromRequirement(t *testing.T) {
	dr := `identifier "com.anthropic.claudefordesktop" and anchor apple generic and certificate leaf[subject.OU] = "Q6L2SF6YDW"`
	if got := upstreamSigningLabel(dr); got != "Q6L2SF6YDW" {
		t.Fatalf("upstreamSigningLabel = %q, want Q6L2SF6YDW", got)
	}
}

func TestUpstreamSigningLabelFallsBackToRawRequirement(t *testing.T) {
	dr := `identifier "com.openai.codex.beta" and certificate leaf = H[field.1.2.840]`
	if got := upstreamSigningLabel(dr); got != dr {
		t.Fatalf("upstreamSigningLabel = %q, want raw requirement fallback", got)
	}
}

func TestUpstreamSigningLabelMarksUnknownWhenAbsent(t *testing.T) {
	for _, input := range []string{"", "   "} {
		if got := upstreamSigningLabel(input); got != "unknown" {
			t.Fatalf("upstreamSigningLabel(%q) = %q, want unknown", input, got)
		}
	}
}
