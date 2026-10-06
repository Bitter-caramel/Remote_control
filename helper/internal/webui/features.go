package webui

// 功能插件导入清单。
//
// 新增功能时在这里加一行空白导入：
//
//	_ "remoteassist-helper/internal/webui/features/<功能ID>"
//
// 空白导入会触发该功能包的 init()，它内部调用 feat.RegisterFeature 把自己
// 注册进注册表；webui.Register 在启动时统一挂载。完整步骤见
// doc/新增控制台功能插件.md。
import (
	_ "remoteassist-helper/internal/webui/features/file"
)