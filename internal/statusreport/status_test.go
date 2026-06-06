package statusreport

import "testing"

func TestUpstreamSigningLabelExtractsQuotedTeam(t *testing.T) {
	dr := `identifier "com.anthropic.claudefordesktop" and anchor apple generic and certificate leaf[subject.OU] = "Q6L2SF6YDW"`
	if got := upstreamSigningLabel(dr); got != "Q6L2SF6YDW" {
		t.Fatalf("upstreamSigningLabel = %q, want Q6L2SF6YDW", got)
	}
}

func TestUpstreamSigningLabelExtractsBareTeam(t *testing.T) {
	dr := `identifier "com.anthropic.claudefordesktop" and anchor apple generic and certificate leaf[subject.OU] = Q6L2SF6YDW`
	if got := upstreamSigningLabel(dr); got != "Q6L2SF6YDW" {
		t.Fatalf("upstreamSigningLabel = %q, want Q6L2SF6YDW", got)
	}
}

func TestUpstreamSigningLabelExtractsFirstTeamFromCompoundRequirement(t *testing.T) {
	dr := `anchor apple generic and (certificate leaf[subject.OU] = VDXQ22DGB9 or certificate leaf[subject.OU] = DCNK4UB866)`
	if got := upstreamSigningLabel(dr); got != "VDXQ22DGB9" {
		t.Fatalf("upstreamSigningLabel = %q, want VDXQ22DGB9", got)
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
