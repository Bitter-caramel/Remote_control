// Package feat 提供 Web 控制台的「功能插件」注册表与依赖注入。
//
// 为什么单独一个子包：功能子包（features/*）要在 init() 里把自己注册进来，
// 而 webui 主包又要空白导入它们来触发 init —— 若注册表定义在 webui 包里，
// 会形成 webui → features/x → webui 的导入环。放在这里，双向都不成环。
//
// 用法（完整步骤见 doc/新增控制台功能插件.md）：
//
//  1. 新建 helper/internal/webui/features/<id>/ 子包；
//  2. 包内 init() 里调用 feat.RegisterFeature(feat.Manifest{...})；
//  3. 在 helper/internal/webui/features.go 里加一行空白导入 _ ".../features/<id>"。
//
// webui.Register 会调用 feat.MountAll(deps, mux)，按（Order, ID）顺序挂载全部已注册功能。
package feat

import (
	"net/http"
	"sort"
	"sync"

	"remoteassist-helper/internal/auth"
	"remoteassist-helper/internal/core"
	"remoteassist-helper/internal/store"
	"remoteassist-helper/logx"
)

// Deps 功能可用的依赖集合（由 webui.Register 注入，等价于原 *Server 的字段）。
// 新增依赖时在这里加字段，功能包按需取用；保持只增不改，避免波及已迁移的功能。
type Deps struct {
	M    *core.Manager
	Prog *logx.ProgramLog
	St   *store.Store
	Auth *auth.Authenticator
}

// Manifest 一个功能插件的元信息与挂载函数。
// ID 必须全局唯一；Mount 里把本功能的路由挂到 mux 上。
type Manifest struct {
	ID    string // 唯一标识，如 "file"
	Name  string // 展示名，如 "文件传输"
	Layer string // 分层：L0 内核 / L1 基础 / L2 具体 / L3 用户扩展
	Order int    // 挂载顺序（小的先挂）
	Mount func(d Deps, mux *http.ServeMux)
}

var (
	regMu    sync.Mutex
	regItems = make(map[string]Manifest)
	regOrder []string
)

// RegisterFeature 注册一个功能插件（在功能包的 init() 里调用）。
// ID 重复或 Mount 缺失时 panic，尽早暴露错误而不是静默失效。
func RegisterFeature(m Manifest) {
	if m.ID == "" || m.Mount == nil {
		panic("feat.RegisterFeature: 必须提供 ID 与 Mount")
	}
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := regItems[m.ID]; dup {
		panic("feat.RegisterFeature: 功能 ID 重复: " + m.ID)
	}
	regItems[m.ID] = m
	regOrder = append(regOrder, m.ID)
}

// MountAll 按（Order, ID）顺序把全部已注册功能挂到 mux 上。
func MountAll(d Deps, mux *http.ServeMux) {
	regMu.Lock()
	items := make([]Manifest, 0, len(regItems))
	for _, id := range regOrder {
		items = append(items, regItems[id])
	}
	regMu.Unlock()

	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Order != items[j].Order {
			return items[i].Order < items[j].Order
		}
		return items[i].ID < items[j].ID
	})
	for _, m := range items {
		m.Mount(d, mux)
	}
}
