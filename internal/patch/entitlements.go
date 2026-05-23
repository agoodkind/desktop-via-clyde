package patch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

const emptyEntitlementsXML = `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict></dict></plist>`

// augmentEntitlements applies the target-declared entitlement policy. The
// text-level approach matches the existing tool's behavior; the plist is small
// and produced by codesign with a deterministic shape, so regex stripping and
// insertion are scoped to the registered targets.
func augmentEntitlements(in []byte, policy targets.EntitlementsPolicy) ([]byte, error) {
	s := string(in)

	for _, key := range policy.Strip {
		stripped, err := stripEntitlementKey(s, key)
		if err != nil {
			return nil, fmt.Errorf("strip entitlement %s: %w", key, err)
		}
		s = stripped
	}

	for _, key := range policy.RequiredBooleanEntitlements {
		updated, err := ensureBooleanEntitlement(s, key)
		if err != nil {
			return nil, fmt.Errorf("ensure entitlement %s: %w", key, err)
		}
		s = updated
	}

	return []byte(s), nil
}

func ensureBooleanEntitlement(s string, key string) (string, error) {
	if hasBooleanEntitlement([]byte(s), key) {
		return s, nil
	}
	if strings.Contains(s, "<key>"+key+"</key>") {
		return "", fmt.Errorf("entitlement exists but is not true")
	}
	idx := strings.LastIndex(s, "</dict>")
	if idx < 0 {
		return "", fmt.Errorf("could not locate </dict> in entitlements xml")
	}
	newEntry := fmt.Sprintf("\t<key>%s</key>\n\t<true/>\n", key)
	return s[:idx] + newEntry + s[idx:], nil
}

func hasBooleanEntitlement(in []byte, key string) bool {
	pattern := `<key>\s*` + regexp.QuoteMeta(key) + `\s*</key>\s*<true\s*/>`
	re := regexp.MustCompile(pattern)
	return re.Match(in)
}

func hasEntitlementKey(in []byte, key string) bool {
	pattern := `<key>\s*` + regexp.QuoteMeta(key) + `\s*</key>`
	re := regexp.MustCompile(pattern)
	return re.Match(in)
}

func writeAugmentedEntitlementsFile(
	r *Runner,
	label string,
	source string,
	policy targets.EntitlementsPolicy,
) (string, error) {
	return writeAugmentedEntitlementsFileWithFallback(r, label, source, policy, false)
}

func writeAugmentedEntitlementsFileAllowEmpty(
	r *Runner,
	label string,
	source string,
	policy targets.EntitlementsPolicy,
) (string, error) {
	return writeAugmentedEntitlementsFileWithFallback(r, label, source, policy, true)
}

func writeAugmentedEntitlementsFileWithFallback(
	r *Runner,
	label string,
	source string,
	policy targets.EntitlementsPolicy,
	allowEmpty bool,
) (string, error) {
	target := filepath.Join(os.TempDir(),
		"dvc-"+safeTempLabel(label)+"-ent."+strconv.FormatInt(time.Now().UnixNano(), 10)+".plist")
	r.Note("%s: extract entitlements from %s -> %s", label, source, target)
	if r.DryRun {
		return target, nil
	}
	cmd := exec.Command("/usr/bin/codesign", "-d", "--entitlements", "-", "--xml", source)
	out, err := cmd.Output()
	if err != nil {
		if allowEmpty {
			r.Note("%s: using empty entitlements fallback after codesign read failed: %v", label, err)
			out = []byte(emptyEntitlementsXML)
		} else {
			return "", fmt.Errorf("codesign -d --entitlements failed for %s: %w", source, err)
		}
	}
	if len(out) == 0 {
		if allowEmpty {
			r.Note("%s: using empty entitlements fallback", label)
			out = []byte(emptyEntitlementsXML)
		} else {
			return "", fmt.Errorf("codesign produced empty entitlements blob for %s", source)
		}
	}
	if len(out) == 0 {
		return "", fmt.Errorf("codesign -d --entitlements failed for %s: %w", source, err)
	}
	augmented, err := augmentEntitlements(out, policy)
	if err != nil {
		return "", fmt.Errorf("augment entitlements: %w", err)
	}
	if err := os.WriteFile(target, augmented, 0o644); err != nil {
		return "", err
	}
	return target, nil
}

func safeTempLabel(label string) string {
	var b strings.Builder
	for _, r := range label {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func verifyRequiredEntitlements(r *Runner, t targets.Target) error {
	if t.Entitlements == nil {
		return fmt.Errorf("target %s has no entitlement policy", t.ID)
	}
	if r.DryRun {
		return nil
	}
	main := paths.MainBinaryPath(t)
	r.Note("target=%s step 9: verify required entitlements on %s", t.ID, main)
	if err := verifyEntitlementPolicy(r, main, *t.Entitlements); err != nil {
		return err
	}
	realPath := paths.RealBinaryPath(t)
	if _, err := os.Stat(realPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat real binary %s: %w", realPath, err)
	}
	r.Note("target=%s step 9: verify required entitlements on %s", t.ID, realPath)
	return verifyEntitlementPolicy(r, realPath, *t.Entitlements)
}

func verifyEntitlementPolicy(r *Runner, codePath string, policy targets.EntitlementsPolicy) error {
	if err := verifyBooleanEntitlements(r, codePath, policy.RequiredBooleanEntitlements); err != nil {
		return err
	}
	return verifyAbsentEntitlements(r, codePath, policy.Strip)
}

func verifyBooleanEntitlements(r *Runner, codePath string, required []string) error {
	if len(required) == 0 || r.DryRun {
		return nil
	}
	out, err := r.RunCaptureStdout("/usr/bin/codesign", "-d", "--entitlements", "-", "--xml", codePath)
	if err != nil {
		return fmt.Errorf("read entitlements from %s: %w", codePath, err)
	}
	for _, key := range required {
		if !hasBooleanEntitlement(out, key) {
			return fmt.Errorf("%s missing required entitlement %s", codePath, key)
		}
	}
	return nil
}

func verifyAbsentEntitlements(r *Runner, codePath string, absent []string) error {
	if len(absent) == 0 || r.DryRun {
		return nil
	}
	out, err := r.RunCaptureStdout("/usr/bin/codesign", "-d", "--entitlements", "-", "--xml", codePath)
	if err != nil {
		return fmt.Errorf("read entitlements from %s: %w", codePath, err)
	}
	for _, key := range absent {
		if hasEntitlementKey(out, key) {
			return fmt.Errorf("%s still has stripped entitlement %s", codePath, key)
		}
	}
	return nil
}

// stripEntitlementKey removes a `<key>NAME</key>` element and the following
// value element. It accepts both formatted XML (each element on its own line
// with indentation, as Apple's pretty-printed plists ship) and compact XML
// (everything on one line with no inter-element whitespace, as codesign
// `-d --entitlements - --xml` emits). Whitespace between elements is matched
// with `\s*` rather than `\n`, and there is no `^` anchor on the key tag.
//
// Supported value shapes: self-closing `<true/>` / `<false/>`; single-line
// scalars `<string>...</string>` / `<integer>...</integer>` /
// `<real>...</real>` / `<date>...</date>` / `<data>...</data>`; and the
// containers `<array>...</array>` and `<dict>...</dict>` (`?s` lets `.` cross
// newlines; non-greedy stops at the first close tag, matching only the
// element that immediately follows the key, not the enclosing dict).
//
// Trailing whitespace and one optional newline are also consumed so the
// surrounding XML stays well-formed.
func stripEntitlementKey(s, key string) (string, error) {
	q := regexp.QuoteMeta(key)
	keyTag := `<key>` + q + `</key>\s*`
	trail := `\s*\n?`
	patterns := []string{
		keyTag + `<(?:true|false)/>` + trail,
		keyTag + `<string>[^<]*</string>` + trail,
		keyTag + `<integer>[^<]*</integer>` + trail,
		keyTag + `<real>[^<]*</real>` + trail,
		keyTag + `<date>[^<]*</date>` + trail,
		keyTag + `<data>[^<]*</data>` + trail,
		`(?s)` + keyTag + `<array>.*?</array>` + trail,
		`(?s)` + keyTag + `<dict>.*?</dict>` + trail,
	}
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return s, fmt.Errorf("compile %q: %w", p, err)
		}
		s = re.ReplaceAllString(s, "")
	}
	return s, nil
}
