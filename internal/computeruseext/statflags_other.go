//go:build !darwin

package computeruseext

import "io/fs"

func statFlagsString(fs.FileInfo) string {
	return "unknown"
}
