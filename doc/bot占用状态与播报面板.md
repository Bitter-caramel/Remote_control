# Bot 占用状态 / 预定排队 / 底部面板 / 闲置下线

## 一、背景与目标

Web 控制台目前允许**多人同时接入同一台机器并都能输入**（互相干扰）。本次要把它改造成**独占控制 + 排队预定**，
并补上三块配套能力：

1. **占用状态展示**：机器列表每台机器下方显示「用户A 占用」/「无人占用」，并显示排队人数。
2. **预定（排队）模式**：每台 bot 一条 FIFO 队列。空闲时接入即控制；被占用时自动排队，前面的人释放后自动轮到自己。
3. **底部面板（仿 VSCode panel）**：可折叠、自带内部 tab 导航栏、**可扩展注册**（后续还要往里加功能）；
   第一个 tab 是「我的预定」。
4. **闲置下线确认**：占用者 5 分钟「用户无输入 **且** 机器无输出」→ 服务端通知「是否下线？」+ 1 分钟倒计时；
   确认或超时即释放，点「否」则重新计 5 分钟。**目的是不打断装环境、装依赖这类长时间无输入但有输出的任务。**
5. **播报面板**：单独导航入口，记录占用/释放历史，**点进去才拉取**（HTTP GET，不做常驻推送），
   按机器 `min_role` 过滤。

已确认的取舍：排队适用于**所有接入者（含本机 DOS 窗口）**；排队期间**能看画面但不能输入**；
轮到时**自动获得控制权并弹提示**；底部面板只做「我的预定」。

## 二、核心状态模型（每台 bot）

```
holder  : 当前占用者（至多 1 个订阅者）
queue   : FIFO 等待队列
其他     : 观察者（Web 角色 1，只读旁观，不入队）
```

**接入决策**（在 `SubscribeAs` 内原子完成）

| 接入者 | 条件 | 结果 |
|---|---|---|
| Web 观察者（角色 1） | 总是 | `observer`，不入队 |
| 可操作者（Web 角色 ≥ 2 / 本机终端） | `holder` 为空 | 立即成为 `holder` |
| 可操作者 | `holder` 非空 | 加入队尾，`waiting` |

**输入路由**：所有人（含 `waiting`、`observer`）都收到终端输出；**只有 `holder` 的输入被转发**。
观察者与排队者的输入在服务端直接丢弃（复用现有 `if !id.CanOperate() { continue }` 的位置）。

**释放**（断开 / 主动放弃 / 闲置超时确认 / 机器下线）：若是 `holder`，从队首提升下一个存活的可操作者，
推 `granted` 通知，并记一条播报事件。排队位次 = 队列下标 + 1，有人离开自动前移。

**机器离线/被顶替**：`Remove()` → `CloseSubs(reason)` 统一清理，不为下一位提升。

## 三、后端改动

### 1. `helper/internal/core/bot.go` —— 订阅者带身份 + 控制权仲裁

现状：`subs map[int]chan []byte` + `subMu`（L40-43），`Subscribe()`（L106-127）、`PushOut()`（L131-145）、`CloseSubs()`（L148-155）。

- `subs` 改为 `map[int]*subEntry`：
  ```go
  type subEntry struct {
      ch    chan []byte      // 终端输出（256 缓冲，沿用现有值）
      ctrl  chan ControlMsg  // 定向控制消息（小缓冲 8，非阻塞投递，满了丢弃）
      who   Subscriber
      mode  ControlMode      // observer / waiting / operator
  }
  ```
- `BOT` 增加 `holder int`（-1 表示空闲，`NewBOT` 里初始化）、`queue []int`、`lastActive atomic.Int64`、
  `idleAsked bool` / `idleDeadline time.Time`。全部由现有 `subMu` 一并保护。
- **保留 `Subscribe()` 作为包装**，转调新增的 `SubscribeAs(who)`：
  ```go
  func (b *BOT) Subscribe() (*Subscription, error) {
      return b.SubscribeAs(Subscriber{Kind: SubLocal, Display: "本机终端"})
  }
  func (b *BOT) SubscribeAs(who Subscriber) (*Subscription, error)
  ```
  本机终端**也要排队**（用户已确认），所以不再设 `Operate` 直通标志。
- `Subscription` 结构：`Out <-chan []byte`、`Ctrl <-chan ControlMsg`、`Mode`、`Queue`、`Close func()`。
  返回结构而非裸 channel，是因为调用方需要知道自己是占用者还是第几位排队者。
- `Close()` 闭包：**只有真的从 map 里删掉了**才 `close` 并处理让位（避免与 `CloseSubs` 重复）。
  让位逻辑先收集"要提升的 subID"、释放 `subMu`、再投递 `granted` 与写事件，避免锁内做投递。
- `CloseSubs(reason string)`：签名加参数，两个调用点（`manager.go` L78、L97）同步改；
  遍历时清理 `holder`/`queue`，为每个 `operator` 记一条释放事件，**不做提升**。
- `PushOut`：广播给所有订阅者（不变）+ 刷新 `lastActive`。
- 新增方法：
  - `Occupancy() Occupancy` —— 锁内 O(订阅者数) 遍历去重，**不做 IO**
  - `Reservations() []Reservation` —— 当前 holder 与排队者（含位次）
  - `Touch()`、`RevokeHolder(reason)`、`AnswerIdle(subID int, keep bool)`
  - `SetEventLog(*EventLog)`（由 `Manager.Register` 注入，允许 nil 以兼容测试直接构造的 BOT）
- **闲置时间戳的唯一挂点**：`Send()` 内当 `msg.Type == protocol.TypeInput` 时 `b.Touch()`。
  已核实终端输入的三条路径（`webui/server.go:345`、`ipc/server.go:134`、`panel/bot.go:94/158`）
  都汇聚到 `b.Send(TypeInput)`，**这三处一行都不用改**；输出侧挂在 `PushOut`。
  由此天然满足「用户无输入 **且** 机器无输出」才算无操作。

### 2. `helper/internal/core/occupancy.go`（新建）—— 对外模型

```go
type SubKind int
const ( SubLocal SubKind = iota; SubBrowser )

type ControlMode int
const ( ModeObserver ControlMode = iota; ModeWaiting; ModeOperator )

type Subscriber struct {
    Kind     SubKind
    UserID   int64
    Username string // 去重键；本机终端为空
    Display  string // 展示名；本机终端固定「本机终端」
}

type ControlMsg struct {          // 定向推送给单个订阅者
    Type     string `json:"type"`     // granted | queued | idle_prompt | revoked
    Text     string `json:"text"`
    Queue    int    `json:"queue"`    // 排队位次
    Holder   string `json:"holder"`   // 当前占用者展示名
    Deadline int64  `json:"deadline"` // idle_prompt 的 Unix 秒
}

type Occupancy struct {
    Occupied  bool     `json:"occupied"`
    Text      string   `json:"text"`      // "无人占用" / "张三 占用" / "张三、李四 占用" / "张三 等 3 人占用"
    Occupants []string `json:"occupants"`
    Observers int      `json:"observers"`
    Queue     int      `json:"queue"`     // 排队人数
}

type Reservation struct {
    BotID string `json:"bot_id"`
    BotName string `json:"bot_name"`
    Mode  string `json:"mode"`      // operator | waiting
    Queue int    `json:"queue"`
    Holder string `json:"holder"`
    IdleDeadline int64 `json:"idle_deadline"` // 0 = 未在询问
}
```

**占用文本只有 4 档**（服务端算好）：无人占用 / 单人 / 两人并列 / ≥3 人「等 N 人」。
同一账号多标签页按 `Username` 去重；本机终端多窗口合并为一个「本机终端」。
观察者**不改变**占用文本，只贡献 `observers` 计数，前端在 >0 时追加灰色尾巴「· 2 人观察中」。

### 3. `helper/internal/core/events.go`（新建）—— 占用/释放历史

内存**定长环形缓冲**，`eventRingSize = 500`（单条约 200 B，合计约 100 KB）。
不落 SQLite 的理由：事件只在「接入/离开终端、排队变动」时发生，频次极低，落库要引入表结构迁移与每次连接写盘，
收益不匹配；定长环保证内存上界、O(1) 追加。

```go
type EventKind string
const (
    EventOccupy  EventKind = "occupy"    // 开始占用
    EventRelease EventKind = "release"   // 释放
    EventQueue   EventKind = "queue"     // 加入排队
)

type Event struct {
    Seq      uint64    `json:"seq"`
    Time     time.Time `json:"time"`
    Kind     EventKind `json:"kind"`
    BotID    string    `json:"bot_id"`
    BotName  string    `json:"bot_name"`  // 名称快照（机器下线后兜底）
    Actor    string    `json:"actor"`     // "张三" / "本机终端"
    Username string    `json:"username"`  // 本机终端为空
    Detail   string    `json:"detail"`    // "占用" / "释放" / "释放（机器离线）" / "排队"
}

func NewEventLog(size int) *EventLog
func (e *EventLog) Append(ev Event)    // 内部补 Seq 与 Time
func (e *EventLog) Snapshot() []Event  // 由新到旧返回副本
```

观察者的进出不记录（避免刷屏）。

### 4. `helper/internal/core/idle.go`（新建）—— 闲置扫描

参数集中一处，便于调：
```go
const (
    IdleTimeout   = 5 * time.Minute
    IdleConfirm   = 1 * time.Minute
    idleScanEvery = 15 * time.Second
)
```
`func (m *Manager) StartIdleWatch()` 起一个 goroutine（写法参照 `core/heartbeat.go` 的 ticker 模式），
由 `app/main.go` 调用（与现有 `startSessionJanitor` 一致）。每轮：

1. 遍历在线 bot，取 holder 与其 `lastActive`
2. 未在询问且 `now - lastActive > IdleTimeout` → 标记 `idleAsked`，置 `idleDeadline = now + IdleConfirm`，
   向该 holder 的 `ctrl` 投递 `idle_prompt`
3. 已在询问且 `now > idleDeadline` → 调 `RevokeHolder("长时间无操作")`（内部释放 → 自动提升下一位）
4. 收到「否」→ `lastActive = now`、清除 `idleAsked`（重新计 5 分钟）

`AnswerIdle(subID, keep)` 由上行消息触发；`keep=true` 表示点「是」（下线），`false` 表示点「否」（继续）。

### 5. `helper/internal/core/manager.go` / `execwait.go`

- `Manager` 加 `events *EventLog`，`NewManager` 初始化，新增访问器 `Events()`。
- `Register()`：登记进 `bots` 后 `b.SetEventLog(m.events)`（此刻尚无订阅者，无竞争，不必改 `NewBOT` 签名）；
  `old.CloseSubs()` → `old.CloseSubs("连接被取代")`。
- `Remove()`：`b.CloseSubs()` → `b.CloseSubs(真实下线原因)`。
- `BotInfo`（`execwait.go` L11-17）加 `Occupancy Occupancy \`json:"occupancy"\``；
  `BotInfos()`（L74-88）在 `m.mu` 内对每台填值。**`handleBots` 的过滤代码不用动**——过滤发生在已带占用的
  `BotInfo` 上，天然满足「看不到的机器连占用也不会出现」。

### 6. `helper/internal/webui/server.go` + `events.go` + `reservations.go`（新建）

新增路由（均需登录）：

```go
mux.Handle("GET  /api/events",       a.RequireAuth(http.HandlerFunc(s.handleEvents)))
mux.Handle("GET  /api/reservations", a.RequireAuth(http.HandlerFunc(s.handleReservations)))
mux.Handle("POST /api/bots/{id}/release", a.RequireAuth(http.HandlerFunc(s.handleBotRelease)))
```

- `handleTerm`（L319）：`b.Subscribe()` → `b.SubscribeAs(authSubscriber(id))`，并在 `select` 里**增加一路
  `case cm := <-sub.Ctrl:`**，序列化成 `wsOutput{Type: "ctrl", Data: <json>}` 下发；
  上行的 `wsInput` 增加 `Type == "idle_answer"` 分支，转 `b.AnswerIdle(...)`。
  输入转发处沿用现有的 `if !id.CanOperate() { continue }`，再叠加 `sub.Mode == ModeOperator` 才转发。
- `handleEvents`：参数 `limit`（默认 100、上限 500），**先按 `st.MinRoles()` 全量等级过滤、后截断**
  （否则低角色用户会被无权记录顶掉配额），无权记录静默丢弃，不返回「被隐藏 N 条」。
- `handleReservations`：只返回**当前用户自己**的占用/排队项（`b.Reservations()` 里筛 `UserID`），
  附带 `idle_deadline` 供前端本地倒计时。
- `handleBotRelease`：当前用户主动放弃该 bot 的控制权（排队中也用于退出队列），随后让位下一位。

`authSubscriber(id)` 小函数：`Display` 取 `DisplayName`，为空退回 `Username`；观察者由此天然落到 `ModeObserver`。

## 四、前端改动

### 1. 占用状态（`assets/js/panel-term.js`）

`renderRows()`（L126-144）在 `row-sub` 后追加占用行，**始终渲染**：
`无人占用` / `张三 占用`（加粗） / 尾部灰色「· 2 人观察中」 / 有人排队时再追加「· 2 人排队」。
数据随现有 `setInterval(refreshNow, 2000)`（L173）自动更新，**不新增任何连接**。

### 2. 终端面板的模式与通知（`assets/js/panel-term.js`）

- 面板顶部或输入区显示当前模式：`占用中` / `排队中（第 2 位）` / `观察者·只读`。
- 输入发送条件从 `state.canOperate` 收紧为「本人是本连接的操作者」（服务端已是最终防线，前端只是不白敲）。
- 处理 `ctrl` 消息：
  | type | 表现 |
  |---|---|
  | `queued` | 提示「已被 张三 占用，已排队，前面还有 N 人」 |
  | `granted` | 提示「轮到你了，已获得控制权」并切换到可输入 |
  | `idle_prompt` | 弹确认条「5 分钟无操作，是否下线？」+ 1 分钟倒计时 + 「是 / 否」；「否」重新计 5 分钟 |
  | `revoked` | 提示被释放的原因 |

### 3. 底部面板 Dock（新建 `js/dock.js` + `js/dock-reservations.js`）

- `dock.js` 提供**与 `registry.js` 同风格的注册机制**：
  ```js
  UI.dock.register({ id, name, order, minRole, mount })
  ```
  外壳读注册表渲染 `#dock-tabs`，点击切换；整个 dock 可展开/收起，**默认收起**
  （VSCode 的 panel 默认也是收起的，避免和终端抢空间）。
- `index.html`：在 `#stage` 之后插入
  ```html
  <section id="dock" class="collapsed">
    <div id="dock-tabs"></div>
    <div id="dock-body"></div>
  </section>
  ```
- `dock-reservations.js`：「我的预定」`{id:"myresv", name:"我的预定", order:10}`。
  打开时拉 `GET /api/reservations`；有 `idle_deadline` 时**本地 1 秒倒计时**（不请求服务器）；
  提供「刷新」与「放弃」（调 `POST /api/bots/{id}/release`）；**不使用 `setInterval` 轮询网络**
  （照抄 `panel-botadmin.js` 的「不轮询」做法）。空态：「当前没有占用或排队中的机器」。
- 脚本顺序（`index.html`）：registry → api → session → **dock** → dock-* → panel-logs → panel-events →
  panel-term/botadmin/users → shell。必须在 `shell.js` 之前注册完。

### 4. 播报面板（新建 `js/panel-events.js`）

`{ id:"events", name:"播报", icon:"◎", order:30 }`，**不设 `minRole`**（否则观察者整个面板会消失；
可见子集由服务端按机器等级过滤）。左列「全部」+ 从**已拉取事件里派生**的机器行（不额外请求 `/api/bots`，
这样已下线机器的历史仍可筛）；主区每条显示 时间 / 详情 / actor / 机器名。

### 5. `assets/js/api.js` / `assets/css/app.css`

- `api.js` 增加 `events(limit)`、`reservations()`、`release(botID)`。
- `app.css` 增加：占用行样式（`.row-sub.occupancy` / `.busy` / `.occ-observer` / `.occ-queue`）、
  dock 样式（`#dock` / `#dock.collapsed` / `#dock-tabs` / `.dock-tab` / `.dock-tab.active` / `#dock-body`）、
  事件列表与排队条目样式、闲置确认条样式。颜色沿用现有 CSS 变量。

## 五、参数与配置

| 常量 | 位置 | 值 | 说明 |
|---|---|---|---|
| `IdleTimeout` | `core/idle.go` | `5m` | 无操作多久后询问是否下线 |
| `IdleConfirm` | `core/idle.go` | `1m` | 询问后的确认倒计时 |
| `idleScanEvery` | `core/idle.go` | `15s` | 扫描周期 |
| `eventRingSize` | `core/events.go` | `500` | 播报历史条数上限 |

## 六、风险与注意事项

1. **锁序**：只存在 `Manager.mu → BOT.subMu → EventLog.mu` 单向嵌套；`Occupancy()`/`Reservations()`
   只做 O(订阅者数) 遍历，**锁内不做 IO、不做投递**；让位投递一律在释放 `subMu` 之后。
2. **释放事件不重复**：`Close()` 闭包只在真的删到条目时才记；`CloseSubs` 只对仍在 map 里的条目记。
3. **让位幂等**：`Remove()` 走的 `CloseSubs` **不做提升**；只有单个订阅者 `Close()` 才提升下一位。
4. **`-cli exec` 不参与排队**：一次性命令不订阅、不占终端（因此在同一机器上仍可能与交互终端互不干扰地并发执行）。
   这是有意的取舍，需在文档中写明。
5. **排队者可能白等**：`waiting` 状态依赖该终端连接存活，连接断开即视为放弃排队并在播报里体现。
6. **名称兜底**：事件里存 `bot_name`/`actor` 快照，渲染时不回查 DB 或 Manager。
7. **历史不持久**：播报仅存内存，helper 重启即清空。
8. **测试影响**：`app/e2e_test.go` 里两个 `fakeClient` 先后接入 —— 第二个将变成**排队者**而非并行操作者，
   断言需要相应调整（`dup` 用例改为验证排队/让位）。

## 七、验证方式

1. `gofmt`、`go vet ./...`、`go test ./...`、Windows + Linux amd64（`CGO_ENABLED=0`）双平台构建。
2. 启动 helper，建普通用户（角色 2）一个、观察者（角色 1）一个。
3. **占用与排队**：浏览器 A 接入 `B1` → 机器列表变「张三 占用」；浏览器 B（同角色）接入同一台 →
   变「张三 占用 · 1 人排队」，B 显示「排队中（第 1 位）」、能看到画面但按键无反应。
4. **让位**：A 关闭页面 → B **自动**收到「轮到你了」并可输入，占用文本变为 B，队列归零。
5. **本机终端也排队**：A 占用时，DOS 面板 `bot B1` 接入 → 显示排队提示而不是抢占。
6. **观察者**：观察者接入 → 文本不变，尾部出现「· 1 人观察中」，输入被丢弃。
7. **闲置下线**：把 `IdleTimeout` 临时调成 20 秒、`IdleConfirm` 调成 10 秒编译验证 ——
   停止输入且让被控端静默 → 收到「是否下线？」；点「否」后计时重来；点「是」或超时 → 释放并让位。
   **关键回归**：让被控端持续输出（如长时间 `ping`）但用户不输入 → **不应**触发询问。
8. **播报与等级过滤**：管理员把 `B1` 的 `min_role` 改为 3 → 普通用户/观察者的机器列表与播报面板里
   `B1` 全部消失，管理员仍可见。
9. **Dock**：展开底部面板 → 「我的预定」列出正在占用/排队的 bot 与倒计时；点「放弃」后队列前移。

## 八、关键文件

- `helper/internal/core/bot.go`（订阅者带身份 + 控制权仲裁，核心改造）
- `helper/internal/core/occupancy.go`、`events.go`、`idle.go`（均新建）
- `helper/internal/core/manager.go`、`execwait.go`
- `helper/internal/webui/server.go`、`events.go`、`reservations.go`（后两个新建）
- `helper/internal/webui/assets/js/`：`panel-term.js`、`panel-events.js`（新）、`dock.js`（新）、
  `dock-reservations.js`（新）、`api.js`、`shell.js`、`index.html`、`css/app.css`
- `helper/internal/app/main.go`（启动闲置扫描）、`helper/internal/app/e2e_test.go`（断言调整）
