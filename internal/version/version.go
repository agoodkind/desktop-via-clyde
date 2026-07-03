// Package version exposes build-time metadata for desktop-via-clyde.
package version

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"

	gklogversion "goodkind.io/gklog/version"
)

// Commit is the git commit SHA stamped at build time.
var Commit = "unknown"

// Version is the semantic or describe-style version stamped at build time.
var Version = "dev"

// Dirty is "true" when the worktree was dirty at build time.
var Dirty = "false"

// BuildTime is the RFC3339 timestamp stamped at build time.
var BuildTime = "unknown"

// BuildHash returns the gklog binary hash stamped by go-makefile.
func BuildHash() string {
	stampedHash := strings.TrimSpace(gklogversion.BinHash)
	if stampedHash != "" && stampedHash != "unknown" {
		return stampedHash
	}
	return runtimeBuildHash()
}

func runtimeBuildHash() string {
	executablePath, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	file, err := os.Open(executablePath)
	if err != nil {
		return "unknown"
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(hash.Sum(nil))[:12]
}

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
