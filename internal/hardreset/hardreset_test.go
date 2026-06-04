package hardreset

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestBuildPlanIncludesConfiguredBundleAliasesAndHelpers(t *testing.T) {
	target := targets.Target{
		ID:                "codex",
		AppPath:           "/Applications/Codex.app",
		BundleID:          "com.openai.codex.beta",
		BundleIDAliases:   []string{"com.openai.codex"},
		HelperBundleIDs:   []string{"com.openai.sky.CUAService", "com.openai.codex.helper"},
		HardResetServices: []string{"ScreenCapture", "SystemPolicyAllFiles"},
	}

	plan, err := BuildPlan(context.Background(), target)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	for _, want := range []string{
		"com.openai.codex",
		"com.openai.codex.beta",
		"com.openai.codex.helper",
		"com.openai.sky.CUAService",
	} {
		if !containsString(plan.BundleIDs, want) {
			t.Fatalf("bundle IDs missing %q: %v", want, plan.BundleIDs)
		}
	}
	if !containsString(plan.Services, "ScreenCapture") {
		t.Fatalf("services missing ScreenCapture: %v", plan.Services)
	}
}

func TestRunDryRunPrintsTCCResetCommandsAndReportOnlyAftercare(t *testing.T) {
	target := targets.Target{
		ID:                "codex",
		AppPath:           "/Applications/Codex.app",
		BundleID:          "com.openai.codex.beta",
		HardResetServices: []string{"ScreenCapture"},
	}

	var out bytes.Buffer
	if err := Run(context.Background(), target, Options{DryRun: true, Out: &out}); err != nil {
		t.Fatalf("Run dry-run: %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"target=codex hard-reset",
		"dry-run: /usr/bin/tccutil reset ScreenCapture com.openai.codex.beta",
		"aftercare=report-only",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("dry-run output missing %q\n%s", want, text)
		}
	}
	for _, forbidden := range []string{"System Settings", "open ", "launchctl"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("dry-run output contains forbidden aftercare %q\n%s", forbidden, text)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
