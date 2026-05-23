// Package launchagent renders the watcher LaunchAgent plist from the template.
package launchagent

import (
	"bytes"
	_ "embed"
	"fmt"
	"text/template"
)

//go:embed io.goodkind.desktop-via-clyde.watcher.plist.in
var templateSource string

// RenderInput is the data passed into the plist template.
type RenderInput struct {
	BinaryPath string
	LogPath    string
}

// Render expands the template with the given binary path and log path.
func Render(in RenderInput) (string, error) {
	if in.BinaryPath == "" {
		return "", fmt.Errorf("launchagent: BinaryPath required")
	}
	tmpl, err := template.New("launchagent").Parse(templateSource)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, in); err != nil {
		return "", err
	}
	return buf.String(), nil
}
