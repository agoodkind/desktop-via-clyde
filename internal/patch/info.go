package patch

import (
	"fmt"
	"os"

	"howett.net/plist"
)

// InfoPlist is the subset of Info.plist that the patcher reads.
type InfoPlist struct {
	CFBundleExecutable string `plist:"CFBundleExecutable"`
	CFBundleVersion    string `plist:"CFBundleVersion"`
	CFBundleIdentifier string `plist:"CFBundleIdentifier"`
	SUPublicEDKey      string `plist:"SUPublicEDKey"`
}

// ReadInfoPlist parses the bundle Info.plist at path.
func ReadInfoPlist(path string) (InfoPlist, error) {
	var out InfoPlist
	data, err := os.ReadFile(path)
	if err != nil {
		return out, logPatchErrorNoContext("patch.info_plist_read_failed", fmt.Errorf("read %s: %w", path, err))
	}
	if _, err := plist.Unmarshal(data, &out); err != nil {
		return out, logPatchErrorNoContext("patch.info_plist_parse_failed", fmt.Errorf("parse %s: %w", path, err))
	}
	if out.CFBundleVersion == "" {
		return out, fmt.Errorf("plist %s missing CFBundleVersion", path)
	}
	return out, nil
}
