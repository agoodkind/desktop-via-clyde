package patch

import "path/filepath"

func nestedCodeSignArgs(id string, entFile string, codePath string) []string {
	if filepath.Ext(filepath.Clean(codePath)) == ".app" && entFile != "" {
		return codesignRuntimeEntitlementsArgs(id, entFile, codePath)
	}
	return []string{
		"--force",
		"--sign",
		id,
		"--options",
		"runtime",
		"--preserve-metadata=entitlements",
		codePath,
	}
}
