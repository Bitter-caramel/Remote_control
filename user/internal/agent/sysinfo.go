package agent

// 被控机系统信息探测：注册时上报给协助者展示。
// 统一格式：<goos>/<goarch> (详细信息)
// 例如：windows/amd64 (10.0.26200)
//
//	linux/amd64 (CentOS Linux 7 (Core), 3.10.0-1160.el7.x86_64)

import "runtime"

// DetectOS 返回系统描述字符串
func DetectOS() string {
	s := runtime.GOOS + "/" + runtime.GOARCH
	if d := osDetail(); d != "" {
		s += " (" + d + ")"
	}
	return s
}
