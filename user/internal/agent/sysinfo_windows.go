//go:build windows

package agent

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// osDetail Windows 详细版本：主.次.构建号，如 10.0.26200
func osDetail() string {
	v := windows.RtlGetVersion()
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
}
