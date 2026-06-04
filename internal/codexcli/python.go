package codexcli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

var (
	pythonCommandPattern  = regexp.MustCompile(`^python3(?:\.\d+)?$`)
	requiresPythonPattern = regexp.MustCompile(`>=\s*(\d+)\.(\d+)`)
)

type pythonProjectMetadata struct {
	Project pythonProjectSection `toml:"project"`
}

type pythonProjectSection struct {
	RequiresPython string `toml:"requires-python"`
}

type pythonVersion struct {
	Major int
	Minor int
}

func (version pythonVersion) String() string {
	return fmt.Sprintf("%d.%d", version.Major, version.Minor)
}

func (version pythonVersion) less(other pythonVersion) bool {
	if version.Major != other.Major {
		return version.Major < other.Major
	}
	return version.Minor < other.Minor
}

func (version pythonVersion) atLeast(other pythonVersion) bool {
	return !version.less(other)
}

func resolveCodexPythonCommand(ctx context.Context, sourceDir string) (string, error) {
	log := codexcliLog.With("function", "resolveCodexPythonCommand")
	requirement, err := readCodexPythonRequirement(sourceDir)
	if err != nil {
		log.ErrorContext(ctx, "codexcli.resolve_python_command.requirement_failed", "err", err)
		return "", err
	}
	command, err := resolveCompatiblePythonCommand(ctx, requirement, os.Getenv("PATH"), lookupPythonVersion)
	if err != nil {
		log.ErrorContext(ctx, "codexcli.resolve_python_command.discovery_failed", "err", err)
		return "", err
	}
	return command, nil
}

func readCodexPythonRequirement(sourceDir string) (pythonVersion, error) {
	log := codexcliLog.With("function", "readCodexPythonRequirement")
	path := filepath.Join(sourceDir, "scripts", "pyproject.toml")
	data, err := os.ReadFile(path)
	if err != nil {
		log.Error("codexcli.read_python_requirement.read_failed", "err", err, "path", path)
		return pythonVersion{}, fmt.Errorf("read Codex scripts pyproject: %w", err)
	}
	var metadata pythonProjectMetadata
	if err := toml.Unmarshal(data, &metadata); err != nil {
		log.Error("codexcli.read_python_requirement.parse_toml_failed", "err", err, "path", path)
		return pythonVersion{}, fmt.Errorf("parse Codex scripts pyproject: %w", err)
	}
	requirement, err := parseRequiresPython(metadata.Project.RequiresPython)
	if err != nil {
		log.Error("codexcli.read_python_requirement.parse_requirement_failed", "err", err, "path", path)
		return pythonVersion{}, fmt.Errorf("parse Codex scripts requires-python: %w", err)
	}
	return requirement, nil
}

func parseRequiresPython(value string) (pythonVersion, error) {
	log := codexcliLog.With("function", "parseRequiresPython")
	matches := requiresPythonPattern.FindStringSubmatch(value)
	if len(matches) != 3 {
		err := fmt.Errorf("unsupported requires-python %q", value)
		log.Error("codexcli.parse_requires_python.unsupported_spec", "err", err)
		return pythonVersion{}, err
	}
	major, err := strconv.Atoi(matches[1])
	if err != nil {
		log.Error("codexcli.parse_requires_python.major_failed", "err", err, "value", matches[1])
		return pythonVersion{}, fmt.Errorf("parse Python major version %q: %w", matches[1], err)
	}
	minor, err := strconv.Atoi(matches[2])
	if err != nil {
		log.Error("codexcli.parse_requires_python.minor_failed", "err", err, "value", matches[2])
		return pythonVersion{}, fmt.Errorf("parse Python minor version %q: %w", matches[2], err)
	}
	return pythonVersion{Major: major, Minor: minor}, nil
}

func resolveCompatiblePythonCommand(
	ctx context.Context,
	requirement pythonVersion,
	pathValue string,
	versionLookup func(context.Context, string) (pythonVersion, error),
) (string, error) {
	candidates := discoverPythonCommandCandidates(pathValue)
	return resolveCompatiblePythonCommandFromCandidates(ctx, requirement, candidates, versionLookup)
}

func resolveCompatiblePythonCommandFromCandidates(
	ctx context.Context,
	requirement pythonVersion,
	candidates []string,
	versionLookup func(context.Context, string) (pythonVersion, error),
) (string, error) {
	if len(candidates) == 0 {
		return "", fmt.Errorf("find Python %s or newer for Codex packaging", requirement.String())
	}
	for _, candidate := range candidates {
		version, err := versionLookup(ctx, candidate)
		if err != nil {
			continue
		}
		if candidate == "python3" && version.atLeast(requirement) {
			return candidate, nil
		}
	}
	bestCommand := ""
	bestVersion := pythonVersion{Major: 0, Minor: 0}
	found := false
	for _, candidate := range candidates {
		version, err := versionLookup(ctx, candidate)
		if err != nil || !version.atLeast(requirement) {
			continue
		}
		if !found || bestVersion.less(version) {
			bestCommand = candidate
			bestVersion = version
			found = true
		}
	}
	if !found {
		return "", fmt.Errorf("find Python %s or newer for Codex packaging", requirement.String())
	}
	return bestCommand, nil
}

func discoverPythonCommandCandidates(pathValue string) []string {
	seen := map[string]bool{}
	candidates := make([]string, 0, 8)
	for _, directory := range filepath.SplitList(pathValue) {
		if directory == "" {
			continue
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if !pythonCommandPattern.MatchString(name) || seen[name] {
				continue
			}
			seen[name] = true
			candidates = append(candidates, name)
		}
	}
	slices.SortStableFunc(candidates, comparePythonCandidateNames)
	return candidates
}

func comparePythonCandidateNames(left string, right string) int {
	if left == "python3" && right != "python3" {
		return -1
	}
	if right == "python3" && left != "python3" {
		return 1
	}
	return strings.Compare(left, right)
}

func lookupPythonVersion(ctx context.Context, name string) (pythonVersion, error) {
	log := codexcliLog.With("function", "lookupPythonVersion")
	log.InfoContext(ctx, "codexcli.lookup_python_version.boundary", "command", name)
	cmd := exec.CommandContext(ctx, name, "-c", "import sys; print(f'{sys.version_info[0]}.{sys.version_info[1]}')")
	output, err := cmd.Output()
	if err != nil {
		log.ErrorContext(ctx, "codexcli.lookup_python_version.command_failed", "err", err, "command", name)
		return pythonVersion{}, fmt.Errorf("read Python version from %s: %w", name, err)
	}
	return parsePythonVersion(strings.TrimSpace(string(output)))
}

func parsePythonVersion(value string) (pythonVersion, error) {
	log := codexcliLog.With("function", "parsePythonVersion")
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		err := fmt.Errorf("unsupported Python version %q", value)
		log.Error("codexcli.parse_python_version.unsupported_value", "err", err)
		return pythonVersion{}, err
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		log.Error("codexcli.parse_python_version.major_failed", "err", err, "value", parts[0])
		return pythonVersion{}, fmt.Errorf("parse Python major version %q: %w", parts[0], err)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		log.Error("codexcli.parse_python_version.minor_failed", "err", err, "value", parts[1])
		return pythonVersion{}, fmt.Errorf("parse Python minor version %q: %w", parts[1], err)
	}
	return pythonVersion{Major: major, Minor: minor}, nil
}
