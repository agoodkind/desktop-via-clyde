// Package version exposes build-time metadata for desktop-via-clyde.
package version

import "strings"

// Commit is the git commit SHA stamped at build time.
var Commit = "unknown"

// Version is the semantic or describe-style version stamped at build time.
var Version = "dev"

// Dirty is "true" when the worktree was dirty at build time.
var Dirty = "false"

// BuildTime is the RFC3339 timestamp stamped at build time.
var BuildTime = "unknown"

// String returns a concise human-readable build identifier for logging.
func String() string {
	version := strings.TrimSpace(Version)
	if version == "" {
		version = "dev"
	}

	commit := strings.TrimSpace(Commit)
	if commit == "" {
		commit = "unknown"
	}
	if commit != "unknown" && len(commit) > 12 {
		commit = commit[:12]
	}

	out := version
	if commit != "unknown" {
		out += " (" + commit
		if Dirty == "true" {
			out += "+dirty"
		}
		out += ")"
	}
	if trimmedBuildTime := strings.TrimSpace(BuildTime); trimmedBuildTime != "" && trimmedBuildTime != "unknown" {
		out += " built " + trimmedBuildTime
	}
	return out
}
