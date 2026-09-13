//go:build !windows

package agent

import (
	"bufio"
	"os"
	"runtime"
	"strings"
)

// osDetail Linux 读取 /etc/os-release 的 PRETTY_NAME + 内核版本（/proc）；
// 其它类 Unix 平台无更多信息可用，返回空。
func osDetail() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	pretty := readOSReleasePretty()
	if kb, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		k := strings.TrimSpace(string(kb))
		if pretty != "" && k != "" {
			return pretty + ", " + k
		}
		if k != "" {
			return k
		}
	}
	return pretty
}

// readOSReleasePretty 解析 /etc/os-release 中的 PRETTY_NAME（去引号）
func readOSReleasePretty() string {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "PRETTY_NAME=") {
			continue
		}
		v := strings.TrimPrefix(line, "PRETTY_NAME=")
		return strings.Trim(v, `"'`)
	}
	return ""
}
