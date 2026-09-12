//go:build !windows

package main

// enableUTF8Console 非 Windows 终端默认 UTF-8，无需处理
func enableUTF8Console() {}
