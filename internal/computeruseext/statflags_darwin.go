//go:build darwin

package computeruseext

import (
	"fmt"
	"io/fs"
	"syscall"
)

func statFlagsString(info fs.FileInfo) string {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "unknown"
	}
	return fmt.Sprintf("0x%x", stat.Flags)
}
