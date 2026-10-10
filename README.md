# Remote_control —— 反向 Shell 远程协助系统（Go 语言实现）

> ## ⚠️ 免责声明（请务必先读）
>
> **本项目仅供学习、研究与技术交流使用，严禁用于任何非法用途。**
>
> - 本项目是一个用 Go 语言编写的**网络编程学习示例**，用于演示 TCP → WebSocket 长连接、
>   反向 Shell（Reverse Shell）通信模型、伪终端（ConPTY）透传、心跳保活与协程管理等技术要点。
> - **仅允许**在你本人拥有**完全所有权与合法管理授权**的机器上部署、运行和测试。
> - **严禁**将本项目用于：未经授权访问、控制、监视他人计算机；窃取数据；破坏系统；
>   搭建僵尸网络；规避安全审计；或任何违反当地法律法规的行为。
> - 未经授权在他人计算机上部署本程序，可能触犯《中华人民共和国刑法》第 285 条
>   （非法侵入计算机信息系统罪、非法获取计算机信息系统数据罪）、
>   第 286 条（破坏计算机信息系统罪）等条款，并可能承担相应民事与刑事责任。
> - 本程序按「原样（AS IS）」提供，作者不对任何因使用或滥用本程序造成的直接、间接损失承担责任。
>   **使用者须自行评估风险，并对自身一切行为独立承担全部法律责任。**
> - 若你不同意上述条款，请立即停止使用并删除本项目全部文件。

---

## 一、项目简介

`Remote_control` 是一个**反向 Shell 架构**的远程控制 / 远程协助系统，由两个互相独立的
Go 程序（两个独立 Go module）组成：

| 角色 | 程序 | 目录 | 运行位置 | 行为 |
|------|------|------|----------|------|
| **操控端 / 协助者端**（Server） | `helper.exe` | [`helper/`](helper/) | 操控者自己的电脑 | **监听端口**，等待被控端连入；提供 DOS 命令行面板，远程下发命令并接收回显 |
| **被控端 / 用户端**（Client） | `user.exe` | [`user/`](user/) | 被协助 / 被控的机器 | **主动外连**操控端；拉起本机常驻交互式终端，把终端输入输出桥接给操控端 |

之所以叫「**反向** Shell」，是因为连接方向是**由被控端主动发起、连向操控端**
（`user.exe` → `helper.exe:8080`），而不是操控端去连被控端。这样被控端无需开放任何入站端口、
无需公网 IP，只要能访问到操控端地址即可建立连接。

核心特性：

- **真正的交互式终端**：不是「一条命令开一个进程」，被控端会拉起一个**常驻的 `cmd.exe` 伪终端**
  （基于 Windows ConPTY），`cd`、环境变量、`diskpart` / `netsh` / `python` 等交互式程序、
  颜色与光标控制序列全部保持，等价于坐在那台机器前敲键盘。
- **多机并发管理**：每台被控机器对应一个独立协程与一个 `BOT` 对象，互不干扰；
  操控端可为不同机器各弹一个独立终端窗口，同时管理多台主机。
- **浏览器控制台（WebUI）**：`helper.exe` 启动后同时是一个 Web 服务，同网段任意设备
  用浏览器打开 `http://<操控端 IP>:8080/` 即可获得仿桌面 IM 三栏界面的控制台，
  无需安装任何客户端，主区是**真正的终端**（xterm.js）而非聊天式输入框。
- **登录鉴权 + 三级角色**：控制台需登录，角色分观察者 / 普通用户 / 管理员；
  每台机器另有可见等级，可按敏感程度限定哪些角色能看到并操作它。账号与审计日志存入本地 SQLite。
- **多操作上下文（每用户一条操作线）**：每个用户对每台机器拥有**独立操作上下文**
  （数据库树形结构 用户ID--botID--操作上下文，对应被控端一个独立 shell 进程）。
  **非观察者用户可同时操控同一台机器**，各自查看自己的操作上下文、互不干扰；
  观看 / 接续他人的操作上下文需发起访问申请并获对方同意；多人可同时观看同一上下文，
  但保留占用状态防止争抢。观察者只读旁观。
  上下文 shell 与被控端连接解耦：断线不销毁，空闲超过 10 分钟由被控端自动回收，避免进程泄漏。
- **文件传输**：Web 控制台的底部 Dock 控制台内置「文件传输」工具（投放 / 下载，
  32KB 分片流式传输，默认落点被控端工作目录下 `received/`）；
  命令行面板也提供 `put` / `get` 命令，两端共用同一条分片通道。
- **唯一身份识别**：机器首次上线由操控端分配一个 `botID`（如 `B0fe9f554`）并保存到被控端本地
  `config.json`，之后靠它识别身份，重连后仍是同一台机器。
- **完整操作留痕**：每台机器有专属日志 `botlogs/<botID>.log`，保存完整终端录像（命令 + 回显）。
- **心跳保活 + 优雅下线**：心跳仅用于探活、不写日志；被控端主动退出时会发送下线通知包。

> 更细的两端说明请分别阅读 [`helper/README.md`](helper/README.md)（操控端）与
> [`user/README.md`](user/README.md)（被控端，含权限最小化与 Windows 安全加固建议）。
> 根目录的 `helper.md`、`user.md` 为早期设计笔记，可作为设计思路的补充参考。

---

## 二、系统架构

### 2.1 总体结构

```
              操控端（协助者）机器                                     被控端机器
┌──────────────────────────────────────────────┐        ┌───────────────────────────────────┐
│  helper.exe                                  │        │  user.exe                         │
│                                              │        │                                   │
│  ┌────────────────────────┐                  │        │                                   │
│  │ Listener  :8080  /ws   │◄──── ① TCP 连接 ─┼────────┼── ① Dial ws://<server_addr>/ws    │
│  │ (listener.go)          │      ② 升级 WS   │        │      (conn.go)                    │
│  └───────────┬────────────┘                  │        │                                   │
│              │ 每连接一个协程                  │        │                                   │
│  ┌───────────▼────────────┐   ③ register ───► │        │                                   │
│  │ Manager (manager.go)   │   ◄─ register_ack │        │                                   │
│  │  bots{}    在线列表    │   ④ heartbeat ──► │        │                                   │
│  │  buffered{} 缓存区列表 │   (每 5s，不写日志)│        │                                   │
│  └───────────┬────────────┘                   │        │                                   │
│              │                                │        │                                   │
│  ┌───────────▼────────────┐   ⑤ input ─────► │        │  ┌─────────────────────────────┐  │
│  │ BOT 对象 (bot.go)      │      resize ────► │        │  │ ShellManager (shell.go)     │  │
│  │  ID/IP/Port/Conn       │   ◄── output ──── │        │  │  ConPTY: cmd.exe /K chcp    │  │
│  │  LastBeat/Missed/blog  │   ⑥ 终端字节流    │        │  │  65001，工作目录=用户主目录 │  │
│  └───────────┬────────────┘                   │        │  └─────────────────────────────┘  │
│              │                                │        │                                   │
│  ┌───────────▼────────────┐                   │        │                                   │
│  │ AttachServer (本地 IPC)│                   │        │                                   │
│  │ 127.0.0.1:<随机端口>   │                   │        │                                   │
│  └───────────┬────────────┘                   │        │                                   │
│              │ ⑦ 本机回环，仅本机可达          │        │                                   │
│  ┌───────────▼────────────┐                   │        │                                   │
│  │ 独立终端窗口进程        │                   │        │                                   │
│  │ helper.exe -attach ... │                   │        │                                   │
│  └────────────────────────┘                   │        │                                   │
│                                               │        │                                   │
│  ⑧ 同一端口 /ws(被控端) + /(浏览器控制台)      │        │                                   │
│  ┌────────────────────────┐  ┌─────────────┐  │        │                                   │
│  │ Web 控制台 (webui/)    │  │ DOS 面板    │  │        │                                   │
│  │  浏览器 ⇄ WS /api/term │  │ (panel/)    │  │        │                                   │
│  └───────────┬────────────┘  └──────┬──────┘  │        │                                   │
│              └──────┬───────────────┘         │        │                                   │
│                     ▼ 都从 Manager 取 BOT      │        │                                   │
│  后台：心跳检测 (heartbeat.go) / 监听          │        │                                   │
└──────────────────────────────────────────────┘        └───────────────────────────────────┘
```

> 被控端接入（`/ws`）与浏览器控制台（`/`、`/api/*`）**共用同一个监听端口**：
> 由 `internal/app` 装配时用一个 `http.ServeMux` 分别挂载，互不干扰。

### 2.2 关键设计

- **面向对象 + 一机一协程**：每台被控机器对应一个 `BOT` 对象，由 `internal/uplink` 中的一个独立
  读循环协程服务；协程之间不互相通信，只通过 `Manager` 的加锁容器协调，简化并发管理。
- **多操作上下文 + 广播**：终端按「操作上下文」组织 —— 每个用户对每台机器在数据库创建一条
  树形结构（用户ID--botID--操作上下文），对应被控端一个**独立 shell 进程**（各自 cwd / 环境 /
  前台程序互不影响）。`BOT` 维护订阅者集合，上下文输出**广播**给该上下文的观看者，并保留输出
  缓冲（新接入者立刻能看到现场，而不是一片空白）。**非观察者用户可同时操控同一台机器**，
  各自查看自己的操作上下文；观看 / 接续他人上下文需发起访问申请并获对方同意；多人可同时观看
  同一上下文，但保留占用状态防止争抢；被观看者完成一次操作后同步至观看者。观察者只读旁观。
- **按职责拆分的子包 + 功能插件化**：操控端核心逻辑按 `core`（状态）/ `uplink`（被控端连入）
  / `ipc`（本机接入）/ `term`（控制台原始模式）/ `panel`（DOS 面板）/ `webui`（浏览器控制台）
  / `app`（启动装配）拆分。Web 控制台后端采用**插件注册表**（`feat` + `features/` 子包
  `init` 自注册，`features.go` 空白导入装配），新增功能只需新建功能子包并加入空白导入，
  不改动 `server.go`；前端面板 / Dock / 控制台工具同样通过注册表自注册。
- **两级容器管理**：`Manager` 持有 `bots`（在线机器）与 `buffered`（疑似掉线、缓存等待的机器）
  两个列表。心跳丢失先进缓存区，连续 3 个周期未恢复才真正销毁协程、标记下线。
- **两端协议完全对称**：`helper/protocol/message.go` 与 `user/protocol/message.go` 是**内容一致**
  的两份拷贝（因两端是独立 module，无法直接共用）。**修改协议时必须同步改两处**，否则会握手失败。
- **二进制安全的终端通道**：终端是原始字节流（含 ANSI 控制序列，且可能在 UTF-8 多字节字符中间分包），
  因此 `input` / `output` 消息的 `data` 字段统一用 **Base64** 承载，避免 JSON 编码破坏二进制数据。
- **本地 IPC 独立于远程连接**：操控端弹出的「终端窗口」是**另一个 helper.exe 进程**（`-attach` 模式），
  它只连本机 `127.0.0.1` 的随机端口，由主进程桥接到对应机器的远程 WebSocket。
  好处是主面板不被终端输入输出占用，可同时开多个窗口管理多台机器。
  浏览器控制台走的是**同一个端口上的 WebSocket**（`/api/term?bot=<ID>`），桥接的是同一套
  `BOT` 订阅者，因此 DOS 窗口与浏览器页面看到的完全是同一份终端。
- **平台相关代码隔离**：两端都用 `_windows.go` / `_other.go` 后缀 + `//go:build` 构建约束
  隔离平台差异。Windows 用 ConPTY、Linux 用 `/dev/ptmx` 伪终端（PTY），功能完整且对等；
  macOS 等其它平台仍可编译（降级为管道 shell、内嵌终端），便于开发自测。
- **登录鉴权 + 三级角色**：操控端自带一套轻量鉴权。口令以 PBKDF2-HMAC-SHA256（每账号独立随机盐）
  哈希存库；登录后下发自实现的 HS256 JWT 会话令牌（`httpOnly` + `SameSite=Strict` Cookie）。
  角色分观察者（只读）/ 普通用户（可操作）/ 管理员（用户与机器管理），**权限以数据库为准**，
  改角色 / 改口令 / 停用会立刻让旧令牌失效。
- **机器可见等级**：每台机器带一个 `min_role`，只有角色值 ≥ 该等级的账号才能看到并操作它，
  可按机器敏感程度分级（例如把某些机器只留给管理员）。
- **本地 SQLite 持久化**：账号、机器等级、登录会话、审计日志存入 `logs/helper.db`。
  选用纯 Go 的 `modernc.org/sqlite`，**不依赖 cgo**，因此 `CGO_ENABLED=0` 的
  Linux 交叉编译仍然可用。

### 2.3 通信协议（`protocol/message.go`）

统一 JSON 消息结构：

| 字段 | 类型 | 说明 |
|------|------|------|
| `type` | string | 消息类型（见下表） |
| `userID` | string | 身份标识：被控端注册时携带，首次为空由操控端分配；`attach` 时承载 botID |
| `data` | string | `input` / `output`：Base64 编码的原始字节；`attach_ack`：结果（`ok` 或错误原因） |
| `cols` / `rows` | int | `resize`：终端窗口列数 / 行数 |

消息类型：

| type 常量 | 值 | 方向 | 说明 |
|-----------|-----|------|------|
| `TypeRegister` | `register` | 被控端 → 操控端 | 身份注册（首次上线 `userID` 为空） |
| `TypeRegisterAck` | `register_ack` | 操控端 → 被控端 | 确认 / 分配 `userID`（= botID） |
| `TypeInput` | `input` | 操控端 → 被控端 | 键盘输入的原始字节，写入远端终端 |
| `TypeOutput` | `output` | 被控端 → 操控端 | 终端输出的原始字节 |
| `TypeResize` | `resize` | 操控端 → 被控端 | 终端窗口尺寸变化 |
| `TypeHeartbeat` | `heartbeat` | 被控端 → 操控端 | 心跳（只探活，**不写任何日志**） |
| `TypeBye` | `bye` | 被控端 → 操控端 / IPC → 终端窗口 | 主动下线通知 / 通知终端窗口机器已下线 |
| `TypeAttach` | `attach` | 终端窗口 → 主程序（本地 IPC） | 请求接入某 bot（`userID`=botID，`data`=令牌） |
| `TypeAttachAck` | `attach_ack` | 主程序 → 终端窗口 | 接入结果（`data` = `ok` 或错误原因） |

> 上述是最基础的四类消息。随功能演进协议已扩展出更多类型，均以两端
> `protocol/message.go` 中的常量定义为准（修改时必须同步改两处），主要包括：
>
> - **一次性命令**：`exec` / `exec_result`（`MsgID` 关联，独立进程执行，不碰交互终端）；
> - **操作上下文（Ctx）系列**：`ctx_open` / `ctx_opened` / `ctx_closed` / `ctx_seg_end`
>   （`CtxID` 标识上下文；`Cols` / `Rows` 同步终端尺寸；`Cwd` 携带上次工作目录用于恢复；
>   `Seq` 为段序号，`ctx_open` 时表示新 shell 的起始段序号，实现段序号跨重启续接）；
> - **文件传输**：`file_put` / `file_put_ack` / `file_get` / `file_get_chunk`
>   （`FileID` 关联会话，`Path` 目标路径，`ChunkSize` / `ChunkSeq` / `ChunkLast` 分片，
>   `Data` 为 Base64 分片内容）；
> - **本地 CLI 对接**：`cli_hello` / `cli_hello_ack` / `cli_list` / `cli_list_ack` /
>   `cli_exec` / `cli_result` / `cli_error`（供 `-cli` 模式与 Agent 脚本调用）。

> 浏览器控制台的终端通道（`WS /api/term?bot=<ID>`）不走上面的 `protocol.Message`，
> 而是用一份更精简的页面内协议：上行 `{"type":"input"|"resize","data":<base64>,"cols":n,"rows":n}`，
> 下行 `{"type":"output"|"bye"|"error","data":"..."}`。它桥接的是操控端**内部**的 `BOT`，
> 与远端协议无关，因此不需要两端同步修改。

### 2.4 心跳与下线机制

| 参数 | 值 | 位置 |
|------|-----|------|
| `HeartbeatInterval`（被控端发送间隔） | 5 秒 | `protocol/message.go` |
| `HeartbeatPeriod`（操控端检测周期） | 10 秒 | `protocol/message.go` |
| `MaxMissed`（允许连续丢失周期数） | 3 | `protocol/message.go` |

流程：

1. 被控端每 5 秒发一次 `heartbeat`，**不写入任何日志**。
2. 操控端每 10 秒扫描一次所有在线机器：
   - 该周期内收到过心跳 → `Recover`：丢失计数清零、移出缓存区；
   - 未收到心跳 → `Miss`：丢失计数 +1，**第 1 次**丢失时把该 `BOT` 指针写入**缓存区列表**
     并继续等待；
   - 连续 **3 个周期**（约 30 秒）未收到 → 销毁该机器对应的协程，从 `bots` 列表与缓存区列表
     中同时删除指针，机器标记下线。
3. 被控端**正常退出**（关闭窗口 / `Ctrl+C`）时会主动发送 `bye` 下线通知包，
   操控端立即感知并销毁协程，无需等待心跳超时。
4. 同一 `botID` 掉线未满 3 个周期就重连时，`Manager.Register` 会先关闭旧连接、再以新连接接管，
   避免同一 ID 出现两个 `BOT` 对象。

### 2.5 终端实现要点

**被控端（`user/`）**

- 按「操作上下文」拉起 shell：每个用户的操作线对应一个**独立 shell 进程**
  （Windows 用 ConPTY 启动 `cmd.exe /K chcp 65001>nul`；Linux 用 `/dev/ptmx` 真 PTY），
  各自 cwd / 环境 / 前台程序互不影响；
- 上下文生命周期与连接解耦：**断线不销毁 shell**，超过 10 分钟无任何活动才回收；
  单机上下文上限 5 个，防止多个 ConPTY 拖垮被控端；
- **提示符哨兵（段切分）**：shell 启动时通过环境变量注入不可见 OSC 序列标记提示符
  （`PROMPT` / `PS1` 带 `\e]1337;RA;<nonce>:<cwd>\a`），把连续输出流切成「段」，
  作为观看同步与命令历史回放的粒度；输出静默超过 800ms 也判定一段结束，
  兼容 vim / top 等长期不返回提示符的全屏程序；
- 需要 **Windows 10 1809 / Server 2019 或更新版本**；老系统自动降级为「常驻 `cmd.exe` + 管道」，
  目录与环境变量仍可保持，但少数直接读控制台的交互式程序不可用；
- **Linux** 启动基于 `/dev/ptmx` 的**真伪终端（PTY）**，回显、Ctrl+C/Ctrl+D 等信号、
  行编辑由内核行规程完成，体验与坐在本机终端前一致（与 Windows ConPTY 对等）；
- 其它类 Unix 平台（开发自测用）降级为 `sh -i` 管道（无回显、无信号转换）。

**操控端（`helper/`）**

- `bot [botID]` 命令通过 `cmd /c start` 弹出**独立 DOS 窗口**，该窗口进程以 `-attach` 模式启动，
  经本地 IPC 接入目标机器；主面板立即释放、可继续管理其它机器；
- 终端窗口进入 **raw（透传）模式**：按键实时透传（方向键、Tab、`Ctrl+C` 均可），
  远端输出（含颜色 / 光标控制）实时渲染；
- **`Ctrl+C` 透传给远端**（中断远端正在运行的程序），**不会**关闭窗口；
  **`Ctrl+]` 关闭 / 脱离**终端窗口（脱离后远端 shell 继续存活，再次 `bot [ID]` 仍是原现场）；
- **同一台机器可被多个用户同时操作**（多个 DOS 窗口、多个浏览器页面、DOS 窗口与浏览器混用均可）：
  非观察者用户各自拥有/查看自己的操作上下文，输入互不干扰；观看他人的上下文需申请并获同意；
  观察者只读旁观。命令行窗口与浏览器页面桥接的是同一套操作上下文，看到的是同一份终端现场；
- 机器下线时窗口会提示并等待回车，不会闪退；
- 不支持弹窗的平台（或弹窗失败）自动降级为当前窗口内的**内嵌终端**（按行模式，输入 `/quit` 退出）。

**浏览器控制台（`webui/`）**

- 页面前端全部通过 `go:embed` 内嵌进 `helper.exe`（含 xterm.js 5.5 + fit 插件），
  **不依赖外网 CDN**，离线可用；
- 布局仿桌面版 IM 三栏结构：最左导航栏（功能选择）→ 第二列列表（如在线机器）→ 主区内容；
- 「终端」功能主区是一整块 **xterm.js 真终端**，没有聊天式发送框，按键直接透传到远端 shell，
  体验与 DOS 下的 `bot [ID]` 弹窗一致（含 `Ctrl+C`、方向键、Tab 与 ANSI 颜色）；
- **机器列表显示操作上下文概况**：每台机器下列出各操作上下文的占用者与观看者数量；
  观看他人的上下文需发起申请、经对方同意后接入（观察者只读旁观）；
- **底部 Dock 控制台**（终端下方可展开 / 收起，仿 VSCode 的 panel）自带标签栏，
  用 `UI.dock.register({id,name,order,minRole,global,mount})` 注册扩展面板；
  控制台内进一步用 `UI.console.registerTool` 注册工具（工具实现 `applies(os)` 按机器类型过滤）。
  当前提供「我的上下文」（跨机器查看自己的操作上下文）、「文件传输」（投放 / 下载）、
  「播报」（占用 / 请求 / 释放历史）等；控制台收起再展开时保留各工具的表单状态；
- **命令历史回放**：每个上下文按「段」落库（`context_segments` / `replay_settings` 表，
  按策略裁剪存储），重连后自动回放恢复画面；管理员可在面板配置回放策略；
- **会话回收**：上下文 shell 与被控端连接解耦，断线不销毁；空闲超过 10 分钟
  （`sessionIdleTTL`）由被控端后台扫描回收，避免 shell 进程泄漏；单机上下文上限 5 个；
- 前端功能面板通过 `registerPanel()` **自注册**，外壳只读注册表，新增功能不需要改外壳；
- **登录鉴权**：`POST /api/login` / `POST /api/logout` / `GET /api/me`；
  其余 `/api/*` 一律要求登录，未登录返回 401、权限不足返回 403；
- HTTP/WS 接口：`GET /api/bots`（当前角色可见的在线机器）、`GET /api/logs`（日志读取）、
  `GET /api/events`（播报历史）、`GET /api/me/contexts`（我的操作上下文）、
  `GET|POST /api/bots/{id}/ctx`（某机器的上下文列表 / 打开上下文）、
  `POST /api/ctx/{ctxID}/request`、`POST /api/ctx/requests/{id}/answer`、
  `POST /api/ctx/grants/{id}/revoke`（观看申请 / 授权 / 收回）、
  `POST /api/me/watch-default`（默认观看设置）、`WS /api/term?bot=<ID>&ctx=<ctxID>`（终端桥接）；
  管理员另有 `GET /api/bots/all`、`POST /api/bots/{id}/min-role|note`、
  `GET|POST|DELETE /api/users*` 等用户与机器管理接口，以及 `GET|POST /api/replay/settings`
  （命令历史回放策略）；文件传输由功能插件注册：`GET|POST /api/bots/{id}/file`（下载 / 投放）；
- 终端 WebSocket 的 `CheckOrigin` 只放行**同源**来源（无 Origin 的非浏览器客户端放行）；
  被控端接入的 `/ws` 仍保持宽松（被控端凭 `userID` 自称身份，无鉴权）。

**本地 IPC 的安全设计（`ipc/server.go`）**

- 只监听 `127.0.0.1`（外部网络不可达），端口随机分配（`127.0.0.1:0`）；
- 接入必须携带**每次启动重新生成的 16 字节随机令牌**；
- 校验 WebSocket `Origin`：非浏览器客户端（不带 Origin）放行，浏览器场景只放行本机回环来源；
- 接入请求必须在 **5 秒内**作为第一条消息发出，否则断开。

---

## 三、目录结构

```
Remote_control/
├── README.md                     # 本文件：项目总览、编译、配置、使用说明与免责声明
├── .gitignore                    # 忽略 bin/、config.json、*.log、agent_endpoint.json、*.db、*.key
├── helper.md / user.md           # 早期设计笔记
├── doc/                          # ── 设计与技术文档 ──
│   ├── 项目技术文档.md            #   技术总览（技术选型、解决的问题、实现细节）
│   ├── 新增控制台功能插件.md      #   后端功能插件开发教程
│   ├── 多上下文控制模型重构.md    #   操作上下文模型设计（已实施）
│   └── bot占用状态与播报面板.md   #   旧「独占控制 + 排队」设计（已被多上下文模型取代）
│
├── scripts/                      # ── 一键构建脚本（输出统一到 bin/）──
│   ├── build-helper-windows.bat      # bin/helper.exe
│   ├── build-helper-linux-amd64.bat  # bin/helper_linux_amd64（纯静态，无需 gcc）
│   ├── build-helper-dll.bat          # bin/helper.dll（需要 gcc）
│   ├── build-user-windows.bat        # bin/user.exe
│   ├── build-user-linux-amd64.bat    # bin/user_linux_amd64（纯静态，无需 gcc）
│   └── build-user-dll.bat            # bin/user.dll（需要 gcc）
│
├── bin/                          # 所有编译产物输出到这里（不入库）
│
├── Agent/                        # ── 给 AI Agent / 自动化脚本的 CLI 对接指南 ──
│   └── README.md                 # helper.exe -cli list/exec 的完整契约与安全红线
│
├── helper/                       # ── 操控端（协助者端 / Server）独立 Go module ──
│   ├── go.mod / go.sum           # module remoteassist-helper
│   ├── cmd/helper/main.go        # 薄入口：仅调用 internal/app.Run()
│   ├── internal/                 # 按职责拆分的子包（新增功能 = 新增子包 + 在 app 接线）
│   │   ├── app/                  #   启动装配（含端到端回归测试）
│   │   │   ├── main.go           #     -attach/-cli 分流 → 日志 → 监听 → WebUI → 控制面板
│   │   │   └── e2e_test.go       #     全链路测试：桥接/鉴权/CLI/多接入/下线/name-os 透传
│   │   ├── core/                 #   在线机器状态与操作上下文（与传输方式无关）
│   │   │   ├── bot.go            #     BOT 对象：操作上下文容器、订阅者集合、输出缓冲、心跳、专属日志
│   │   │   ├── context.go        #     多操作上下文模型：输出路由、段缓冲、输入独占锁（授权判定在 webui）
│   │   │   ├── occupancy.go      #     订阅者身份与终端控制消息（granted / revoked / opened / failed）
│   │   │   ├── events.go         #     接入/离开上下文、取得/释放输入权的播报历史（内存定长环形缓冲）
│   │   │   ├── filexfer.go       #     文件传输待回包注册表 + 逐片等待（WaitFileAck/FetchFileChunk）
│   │   │   ├── manager.go        #     bots 管理器：在线列表 + 缓存区列表、botID 分配、上下线
│   │   │   ├── heartbeat.go      #     心跳检测后台协程（每 10s 扫描一次）
│   │   │   └── execwait.go       #     exec 待回包注册表 + bot 摘要信息
│   │   ├── uplink/               #   被控端连入侧
│   │   │   └── listener.go       #     监听端口、HTTP 路由装配、/ws 升级与每台机器的读循环
│   │   ├── ipc/                  #   本机接入
│   │   │   ├── server.go         #     本地 IPC 服务：终端窗口接入、令牌鉴权、输入输出桥接
│   │   │   ├── cli.go            #     /cli 端点：Agent 查询/执行通道
│   │   │   ├── agentcli.go       #     -cli 模式：list/exec 机器可读命令行
│   │   │   ├── client.go         #     -attach 模式：独立终端窗口进程
│   │   │   └── window_*.go       #     Windows 弹窗实现 / 非 Windows 降级
│   │   ├── term/                 #   控制台原始模式（UTF-8 代码页、VT 透传、Ctrl+] 脱离键）
│   │   ├── panel/                #   前台 DOS 命令面板（含内嵌终端降级 + put/get 文件命令）
│   │   ├── store/                #   本地 SQLite：schema.sql + 账号/机器/会话/审计/上下文/回放查询
│   │   ├── auth/                 #   登录鉴权：PBKDF2 口令 / HS256 令牌 / 密钥 / 鉴权中间件
│   │   └── webui/                #   浏览器控制台（后端功能已插件化）
│   │       ├── server.go         #     HTTP/WS 接口 + go:embed 装配 + 登录与机器/日志接口
│   │       ├── admin.go          #     管理员接口：用户管理、机器分级
│   │       ├── context_api.go    #     操作上下文接口（我的上下文、访问申请、授权管理、默认可看）
│   │       ├── replay_api.go     #     命令历史回放接口（回放策略配置）
│   │       ├── events.go         #     播报历史接口（按机器等级过滤）
│   │       ├── features.go       #     功能插件空白导入清单（装配 features/ 下各包）
│   │       ├── feat/             #     功能插件注册表：Deps/Manifest/RegisterFeature/MountAll + HTTP 工具
│   │       ├── features/         #     功能子包（init 自注册，如 features/file 文件传输）
│   │       └── assets/           #     内嵌前端：index.html / css / js / vendor(xterm.js)
│   ├── protocol/message.go       # 两端共用的消息协议（JSON + Base64 字节流）
│   └── logx/                     # programlog + botslog + 每 bot 的 botlog（终端录像）
│
└── user/                         # ── 被控端（用户端 / Client）独立 Go module ──
    ├── go.mod / go.sum           # module remoteassist-user
    ├── cmd/user/main.go          # 薄入口：仅调用 internal/agent.Run()
    ├── internal/                 # 按职责拆分的子包
    │   ├── agent/                #   顶层装配：主循环（连接→注册→shell→心跳）+ 重连
    │   │   ├── main.go           #     Run()：读/建 config、信号处理、自动重连循环
    │   │   └── daemon.go         #     daemon 三入口的薄转接（避免 agent ↔ daemon 成环）
    │   ├── core/                 #   连接与身份
    │   │   ├── config.go         #     config.json：server_addr / user_id / name
    │   │   ├── conn.go           #     Client、Dial、注册、读循环、一次性命令分发
    │   │   └── heartbeat.go      #     周期心跳
    │   ├── term/                 #   多操作上下文 shell（每用户一条操作线）
    │   │   ├── session.go        #     SessionManager：上下文开/复用/回收、段序号、静默兜底封段
    │   │   ├── sentinel.go       #     提示符哨兵解析器（env 注入，把输出切为「段」）
    │   │   ├── shell.go          #     Shell 接口、管道降级实现
    │   │   ├── shell_windows.go  #     Windows ConPTY 优先 + 管道降级
    │   │   ├── shell_other.go    #     其它类 Unix 管道降级
    │   │   └── pty_unix.go       #     Linux 真 PTY（/dev/ptmx）
    │   ├── exec/agentexec.go     #   一次性命令执行（独立进程，不碰交互终端）
    │   ├── sys/                  #   平台系统能力
    │   │   ├── sysinfo*.go       #     系统探测：Windows 版本号 / Linux os-release + 内核
    │   │   ├── console_*.go      #     控制台 UTF-8 代码页
    │   │   └── signals_*.go      #     退出 / 忽略信号集合
    │   └── daemon/               #   后台运行：start / stop / status + pid 文件
    │       ├── daemon.go
    │       └── daemon_*.go       #     Windows / Unix 平台差异
    └── protocol/message.go       # 与操控端一致的消息协议（两份拷贝，需同步修改）
```

**运行期自动生成的目录与文件**（均不入库）

```
helper 进程的当前工作目录/
├── logs/
│   ├── program.log               # 程序运行日志（INFO + ERROR）
│   ├── agent_endpoint.json       # 本地 CLI/Agent 接入点（地址 + 随机令牌），退出时删除
│   ├── helper.db                 # 本地 SQLite：账号、机器等级、登录会话、审计日志（WAL 模式）
│   ├── helper.key                # 会话令牌签名密钥（首次启动生成，权限 0600，敏感！）
│   ├── bots.log                  # 所有连接过的主机：时间 | botID | 名称 | 系统 | 地址 | botlog
│   └── botlogs/<botID>.log       # 每台机器的专属日志（完整终端录像 + [AGENT] 审计行）
└── （CLI 自动向上/向 bin 目录旁查找 logs/agent_endpoint.json）

user 进程的当前工作目录/
└── config.json                   # {"server_addr":"...", "user_id":"Bxxxxxxxx", "name":"自定义名称"}
```

> 注意：日志目录 `logs/`、`config.json`、Shell 的初始工作目录等**相对路径均以进程的当前工作目录为基准**。
> 双击运行时工作目录一般等于 exe 所在目录；若通过计划任务 / 服务方式启动，需注意工作目录可能不同。

---

## 四、编译方式

### 4.1 前置要求

- **Go 工具链**：两个 module 的 `go.mod` 均声明 `go 1.26.5`，请安装不低于该版本的 Go；
  若使用较旧版本，建议启用 toolchain 自动下载，或手动把 `go.mod` 中的版本号下调后重试。
- **操作系统**：主要面向 **Windows** 与 **Linux**（两端伪终端均为完整实现：
  Windows ConPTY、Linux /dev/ptmx）；macOS 等其它平台也可编译
  （功能降级：管道 shell + 内嵌终端），可用于开发自测。
- **网络依赖**（首次编译会自动拉取）：

  | 端 | 依赖 | 版本 | 用途 |
  |----|------|------|------|
  | helper | `github.com/gorilla/websocket` | v1.5.3 | WebSocket 长连接 |
  | user | `github.com/UserExistsError/conpty` | v0.1.4 | Windows ConPTY 伪终端 |
  | user | `github.com/gorilla/websocket` | v1.5.3 | WebSocket 长连接 |
  | user | `golang.org/x/sys` | v0.8.0 | ConPTY 间接依赖 |

### 4.2 一键脚本（推荐）

双击 `scripts/` 下对应脚本即可，产物统一输出到仓库根目录 `bin/`：

| 脚本 | 产物 |
|------|------|
| `scripts/build-helper-windows.bat` | `bin/helper.exe` |
| `scripts/build-user-windows.bat` | `bin/user.exe` |
| `scripts/build-helper-linux-amd64.bat` | `bin/helper_linux_amd64`（纯静态，CentOS 等直接可跑） |
| `scripts/build-user-linux-amd64.bat` | `bin/user_linux_amd64`（纯静态） |
| `scripts/build-*-dll.bat` | `bin/*.dll`（`-buildmode=c-shared`，需 gcc/mingw） |

### 4.3 手动编译

两端各自是**独立的 Go module**，入口包在 `cmd/` 下：

```powershell
cd D:\AAAGitProject\Remote_control\helper
go build -o ..\bin\helper.exe ./cmd/helper

cd ..\user
go build -o ..\bin\user.exe ./cmd/user
```

编译时关闭控制台窗口（可选，纯后台运行，**不推荐 helper 使用**——控制面板依赖控制台输入）：

```powershell
go build -ldflags="-H windowsgui" -o ..\bin\helper.exe ./cmd/helper
go build -ldflags="-H windowsgui" -o ..\bin\user.exe ./cmd/user
```

> ⚠️ 隐藏窗口后程序在后台静默运行，被控端看不到任何界面，也无法通过关窗口退出，
> 只能从任务管理器结束进程。**请确保被控方知情并同意后再使用此选项。**

### 4.4 交叉编译

```powershell
# Windows 上编译 Linux amd64（脚本已内置这三行）
$env:GOOS="linux"; $env:GOARCH="amd64"; $env:CGO_ENABLED="0"
go -C user build -o ../bin/user_linux_amd64 ./cmd/user
$env:GOOS=$null; $env:GOARCH=$null; $env:CGO_ENABLED=$null
```

```bash
# Linux / macOS 上编译 Windows 版本
cd user
GOOS=windows GOARCH=amd64 go build -o user.exe ./cmd/user
```

### 4.5 运行测试（可选）

操控端内置端到端回归测试，覆盖「注册握手 → 本地 IPC 终端桥接与令牌鉴权 → 同机多接入（多窗口 / 多浏览器）→
机器人下线通知 → 心跳 3 周期销毁」全链路：

```powershell
cd D:\AAAGitProject\Remote_control\helper
go test ./... -v
```

---

## 五、配置项

### 5.1 操控端（helper）

| 配置项 | 位置 | 默认值 | 说明 |
|--------|------|--------|------|
| `ListenAddr` | `helper/internal/app/main.go` 常量 | `":8080"` | 对外监听地址 / 端口；也可用环境变量 `RA_LISTEN`（如 `:18080`）覆盖 |
| `LogDir` | `helper/internal/app/main.go` 常量 | `"logs"` | 日志目录（相对进程工作目录） |
| `DBFile` | `helper/internal/app/main.go` 常量 | `"helper.db"` | 本地 SQLite 文件名（放在 `LogDir` 下）：账号、机器等级、会话、审计 |
| `KeyFile` | `helper/internal/app/main.go` 常量 | `"helper.key"` | 会话令牌签名密钥文件名（放在 `LogDir` 下，权限 `0600`，**敏感勿泄露**） |
| `HeartbeatInterval` | `helper/protocol/message.go` | `5 * time.Second` | **被控端**发送心跳的间隔 |
| `HeartbeatPeriod` | `helper/protocol/message.go` | `10 * time.Second` | **操控端**心跳检测周期 |
| `MaxMissed` | `helper/protocol/message.go` | `3` | 允许连续丢失的周期数，超过即下线 |

改完需**重新编译**。心跳相关常量若修改，必须**同步修改** `user/protocol/message.go` 中的同名常量。

**账号与权限（无需改代码，运行时管理）**

- **首次启动**：程序检测到账号表为空，自动创建管理员 `admin`，**默认口令为 `admin123`**
  （保存在本地 SQLite 中），请登录后立即在「用户管理」面板修改口令；
  若已删除 `logs/helper.db`，重启后会重新生成初始管理员（会清空已有账号与审计记录）。
- **角色**：1 观察者（只读，能旁观终端但输入被丢弃）、2 普通用户（可操作可见范围内的机器）、
  3 管理员（可在「用户管理」「机器管理」面板里管理账号与机器）。
- **机器可见等级**：每台机器有一个 `min_role`（默认 2），只有角色值 ≥ 该等级的账号能看到并操作它，
  由管理员在「机器管理」面板逐台调整。终端长连接每 5 秒复查一次权限，改动即时生效。
- 登录会话默认有效期 `auth.SessionTTL = 12h`（在 `helper/internal/auth/auth.go` 中调整）。

### 5.2 被控端（user）

被控端的配置放在运行目录下的 **`config.json`**（首次运行自动创建，权限 `0644`）：

```json
{
  "server_addr": "10.158.128.48:8080",
  "user_id": "B0fe9f554",
  "name": "财务室-电脑"
}
```

| 字段 | 类型 | 说明 |
|------|------|------|
| `server_addr` | string | 操控端的 `IP:端口`。局域网填内网 IP，跨公网填公网 IP 或内网穿透地址 |
| `user_id` | string | 身份标识（与操控端的 `botID` 是同一个东西）。首次上线由操控端分配并写回本地，之后固定不变；为空表示首次上线 |
| `name` | string | **本机自定义名称**，手动填写即可（不需要程序支持任何设置功能）；协助端的上线提示、`bots` 列表、`-cli list` 均会显示。留空则只显示 botID |

其他相关常量（需改代码后重新编译）：

| 配置项 | 位置 | 默认值 | 说明 |
|--------|------|--------|------|
| `defaultServerAddr` | `user/internal/core/config.go` | `"127.0.0.1:8080"` | 首次生成 `config.json` 时写入的默认操控端地址；**发布前应改为实际部署地址** |
| `configPath` | `user/internal/core/config.go` | `"config.json"` | 配置文件路径（相对进程工作目录） |
| `retryInterval` | `user/internal/agent/main.go` | `5 * time.Second` | 断线后的自动重连间隔 |
| ConPTY 初始尺寸 | `user/internal/term/shell_windows.go` | `120 x 30` | 伪终端默认行列数，接入终端窗口后会被实际窗口尺寸覆盖 |

---

## 六、使用流程

### 6.1 快速开始（六步）

**步骤 1｜编译两端**

```powershell
# 推荐：双击 scripts/build-helper-windows.bat 和 scripts/build-user-windows.bat
# 手动编译：
cd D:\AAAGitProject\Remote_control\helper ; go build -o ..\bin\helper.exe ./cmd/helper
cd D:\AAAGitProject\Remote_control\user   ; go build -o ..\bin\user.exe ./cmd/user
```

**步骤 2｜启动操控端**

在操控者机器上双击 `bin\helper.exe` 或命令行运行：

```powershell
.\bin\helper.exe
```

出现 `assist> ` 提示符即表示程序已在后台监听 `8080` 端口，同时会打印一行
「Web 控制台已就绪: http://<本机IP>:8080/」，用浏览器打开即可进入控制台。
**首次启动**会自动创建初始管理员 `admin`，**默认口令 `admin123`**（见步骤 6）。

**步骤 3｜打通网络（让被控端能连到操控端）**

| 场景 | 做法 |
|------|------|
| 同一局域网 | 被控端直接填操控端的**内网 IP**，如 `192.168.1.100:8080`；并确保操控端 Windows 防火墙放行 8080 入站 |
| 跨公网 | 操控端需有公网 IP，或在路由器上做**端口映射**（把路由器公网 IP 的 8080 转发到内网机器的 8080） |
| 无公网 IP | 使用**内网穿透 / 组网工具**（如 frp、ngrok、ZeroTier、Tailscale）把本地 8080 暴露出去 |

查看本机内网 IP：

```powershell
ipconfig | Select-String "IPv4"
```

放行防火墙端口（以管理员身份运行，可选）：

```powershell
New-NetFirewallRule -DisplayName "RemoteAssist 8080" -Direction Inbound -Protocol TCP -LocalPort 8080 -Action Allow
```

**步骤 4｜配置并启动被控端**

把 `bin\user.exe` 放到被控端某个目录，**首次运行**会在当前工作目录生成 `config.json`：

```powershell
.\user.exe
```

用记事本打开 `config.json`，把 `server_addr` 改成步骤 3 确定的操控端地址（`user_id` 留空），
保存后**重新运行** `user.exe`。看到下面这行即表示连接成功：

```
已连接协助者服务器，等待指令。
```

此时操控端面板的 `bots` 命令中即可看到该机器（已自动分配 `botID`，并写回被控端 `config.json`）。

> **无界面 Linux 服务器**：可改用后台子命令，不占用终端窗口——
> `./user_linux_amd64 start` 后台运行、`status` 查状态、`stop` 停止
> （详见 [`user/README.md`](user/README.md) 的「无界面服务器后台运行」）。

**步骤 5｜远程操作**

在操控端面板中对目标机器开终端：

```
assist> bots
BOTID        地址
B4aeddecf    192.168.1.100:54321

assist> bot B4aeddecf
已在新窗口打开 B4aeddecf 的终端，本窗口可继续执行其它命令
assist>
```

弹出的独立 DOS 窗口就是该机器的远程终端，操作与本地 `cmd` 一致：

- `Ctrl+C` 发送给远端（中断远方正在运行的程序），`Ctrl+]` 关闭 / 脱离该终端窗口；
- 脱离后远端 shell 继续存活，再次 `bot [ID]` 仍是原现场（当前目录、环境变量都还在）。

**步骤 6｜（可选）用浏览器控制台**

`helper.exe` 启动后同时是一个 Web 服务。在**同网段的任意设备**（手机、平板、别的电脑）
用浏览器打开：

```
http://<操控端 IP>:8080/
```

即可看到仿桌面 IM 的三栏控制台 —— 最左导航栏选功能，第二列是当前在线的机器列表，
主区是一整块真终端。点开某台机器即可直接操作，效果与 DOS 窗口完全一致，
且**多个人可以同时打开同一台机器**。

首次打开会要求登录：初始管理员账号 `admin`，**默认口令 `admin123`**（首次启动时已写入数据库）。
登录后可进入管理员的「用户管理」「机器管理」面板新增账号、调整权限与机器可见等级，
并可在「用户管理」中修改 `admin` 自己的口令。不同账号看到的机器列表可能不同
（取决于机器的可见等级）。

> 浏览器控制台与 DOS 面板是同一套内核的两个入口，看的是同一份终端现场。
> 控制台**已启用登录鉴权，但仍没有 TLS**：口令与 Cookie 在网络上以明文传输，
> 请只在可信网络内使用，切勿把 8080 直接暴露到公网。

### 6.2 操控端面板命令

| 命令 | 说明 |
|------|------|
| `help` | 查看全部可用命令 |
| `log -p` | 查看程序运行日志（`program.log`） |
| `log --bots` | 查看 `bots.log`（所有连接过的主机） |
| `log --botlog+ID`（也支持 `log --botlog ID`） | 查看指定 `botID` 的专属日志（完整终端录像） |
| `bots` | 查看当前已连接机器的基础信息（BOTID / 地址） |
| `bots -a` | 查看连接过的全部机器（读 `bots.log`） |
| `bots -l` | 查看在线机器的详细信息（上线时间 / 最近心跳 / 状态 / botlog 文件名） |
| `bot [botID]` | 为该机器弹出独立终端窗口（`Ctrl+]` 关闭；同一台机器可被多个窗口 / 浏览器同时接入） |
| `put [botID] [本地文件] [可选远端路径]` | 投放文件到被控端（默认落点其工作目录下 `received/`） |
| `get [botID] [远端路径] [可选本地保存路径]` | 从被控端下载文件（默认保存到本机 `received/`） |
| `exit` | 退出程序（不再监听端口，直至下次启动） |

### 6.3 被控端退出与重连

- **普通模式**：直接关闭窗口，或按 `Ctrl+C`（程序会先向操控端发送 `bye` 下线通知再退出）。
- **隐藏窗口模式**（`-H windowsgui` 编译）：打开任务管理器结束 `user.exe` 进程。
- **后台模式**（`user start` 启动，适合无界面 Linux 服务器）：执行 `./user_linux_amd64 stop` 优雅停止。
- 连接意外断开（网络抖动、操控端重启）时，被控端会**每 5 秒自动重连一次**，无需人工干预。

### 6.4 日志查看

| 日志 | 路径 | 内容 |
|------|------|------|
| 程序运行日志 | `logs/program.log` | 监听启动、机器上线 / 下线、websocket 升级失败、心跳丢失等（INFO + ERROR） |
| 主机清单 | `logs/bots.log` | 每行一条上线记录：`时间 \| botID \| 地址 \| botlogs/<botID>.log` |
| 机器专属日志 | `logs/botlogs/<botID>.log` | 该机器的完整终端录像（命令 + 回显原样留存）+ 上下线、终端接入 / 脱离等 `[SYS]` 事件；**心跳不写入** |

---

## 七、安全与合规注意事项

> 本节内容与 `helper/README.md` 第五、六章以及 `user/README.md` 第四、五、六章一致，请一并阅读。

1. **明确授权是前提**：只在你拥有合法管理授权的机器上部署本程序。
   未经授权在他人机器上运行，可能构成刑事犯罪。
2. **知情与同意**：被控端对自身机器被远程操作一事必须**完全知情并同意**。
   操控者的每一次操作都等同于被控端本人在操作。
3. **最小权限原则**：被控端**执行权限 = 运行它的 Windows 用户权限**。绝大多数协助场景
   用普通用户运行即可；只有在确需管理员操作时才「以管理员身份运行」，且操作完成后立即关闭程序。
   永远不要为了省事而长期以管理员身份后台运行。
4. **进一步收窄权限（进阶）**：可参考 `user/README.md` 第五章，用
   「专用低权限用户（`net user` / `runas`）+ NTFS 权限（`icacls`）+ 软件限制策略（SRP/AppLocker）
   + 沙箱 / 虚拟机」四层手段限制被控端能做什么。
5. **传输仍是明文的**：两端通过 **WebSocket 明文**传输命令与回显，**没有 TLS 加密**；
   被控端接入的 `/ws` 也**不校验身份**（`CheckOrigin` 放行任意来源，被控端凭 `userID` 自称身份）。
   因此**只应在受信任的内网或 VPN / 加密隧道中使用**，切勿在公网无保护地暴露 8080 端口。
6. **浏览器控制台已启用登录鉴权**：WebUI 的 `/api/*` 需要登录，口令以 PBKDF2-HMAC-SHA256
   （每账号独立随机盐）哈希后存入本地 `helper.db`，会话用 HS256 签名的 JWT 并以
   `httpOnly` + `SameSite=Strict` Cookie 下发；权限分观察者 / 普通用户 / 管理员三级，
   机器也有可见等级，改角色 / 改口令 / 停用会立刻让旧令牌失效。
   但**链路没有 TLS**，口令与 Cookie 仍以明文在网络中传输；请在不可信网络中部署时
   用防火墙 / 反向代理把控制台限制在可信来源，或改用 SSH 隧道等加密通道访问。
   另注意 `logs/helper.key`（令牌签名密钥）与 `logs/helper.db` 属敏感文件，已加入 `.gitignore`，
   请勿泄露或提交到版本库。
7. **及时终止**：协助结束后立即关闭 `user.exe`（任务管理器结束进程），不要长期后台常驻。
8. **审查与留痕**：操控端会完整记录所有命令与回显（`botlogs/<botID>.log`）。
   被控方有权要求操控方提供操作日志，或要求其在协助结束后清除相关日志。
9. **本机 IPC 相对安全，但不等于零风险**：终端窗口 IPC 只监听 `127.0.0.1`、端口随机、
   令牌每次启动重新生成，本机其它程序无法随意接入；但**本机其它进程若已具备调试 / 注入能力，仍可能被滥用**。
10. **本程序不做任何隐蔽驻留**：不写注册表、不安装服务、不设开机自启、不留后门。
   彻底卸载只需结束 `user.exe` 进程并删除 `user.exe` 与 `config.json`。

> 再次强调：**本项目仅供学习研究，禁止用于任何非法用途。**

---

## 八、常见问题（FAQ）

**Q1：`go build` 报 Go 版本过低？**
A：`go.mod` 声明了 `go 1.26.5`。请升级 Go 工具链，或在确认代码兼容的前提下把两个
`go.mod` 中的版本号下调后重试。

**Q2：被控端连不上操控端？**
A：按顺序排查 ——
① `config.json` 中 `server_addr` 是否填对（`Test-NetConnection <ip> -Port 8080` 可测连通性）；
② 操控端 Windows 防火墙是否放行 8080 入站；
③ 跨公网时端口映射 / 内网穿透是否生效；
④ 用命令行运行 `user.exe` 观察具体报错（双击运行时报错窗口会一闪而过）。

**Q3：操控端 `bots` 看不到机器？**
A：确认被控端已运行且已看到「已连接协助者服务器，等待指令。」；确认 `server_addr` 指向正确的
IP:端口；若被控端日志显示连接中断，检查其出站连接是否被安全软件拦截。

**Q4：多人能同时操作同一台机器吗？**
A：可以。**非观察者**用户对每台机器各自拥有**独立操作上下文**（独立 shell / 独立 cwd），
可以同时操作、互不干扰。观看或接续他人的操作上下文需**发起访问申请并获对方同意**；
观察者只读旁观。多个 DOS 窗口与浏览器页面桥接的是同一套操作上下文，
看到的是同一份终端现场。

**Q5：弹不出新终端窗口？**
A：非 Windows 平台或 `cmd /c start` 不可用时，程序会自动降级为**当前窗口内的内嵌终端**
（按行模式，输入 `/quit` 返回主面板）。此外，用 `-ldflags="-H windowsgui"` 编译时
`os.Executable()` 拉起的子进程可能没有可用控制台，也会走降级路径。

**Q6：被控端终端里中文乱码？**
A：程序已在启动时把控制台代码页切到 UTF-8（`chcp 65001`），并且伪终端启动命令同样执行了
`chcp 65001>nul`。若仍乱码，请确认使用的是 **ConPTY 模式**（Windows 10 1809+），
管道降级模式对中文与交互式程序的支持较弱。

**Q7：日志在哪？**
A：`helper.exe` 所在工作目录的 `logs/` 下：`program.log`（程序日志）、`bots.log`（主机清单）、
`botlogs/<botID>.log`（每台机器的终端录像）。

**Q8：想换监听端口 / 心跳参数怎么办？**
A：见「五、配置项」。端口在 `helper/internal/app/main.go` 的 `ListenAddr`（也可用环境变量 `RA_LISTEN` 覆盖）；心跳在两端
`protocol/message.go` 的 `HeartbeatInterval` / `HeartbeatPeriod` / `MaxMissed`。
**两端心跳常量必须保持一致**，改完需分别重新编译。

**Q9：怎么彻底卸载？**
A：关闭 `user.exe` / `helper.exe` 进程，删除程序文件、`config.json` 与 `logs/` 目录即可。
本程序不写注册表、不装服务、不留后门。

**Q10：浏览器打不开控制台 / 打开了看不到机器？**
A：① 确认访问的是**操控端机器的 IP** 而不是 `127.0.0.1`（除非就在本机）；
② 确认 `helper.exe` 正在运行，且启动时打印了「Web 控制台已就绪」；
③ Windows 防火墙需放行 8080 **入站**（见步骤 3 的 `New-NetFirewallRule`）；
④ 机器列表来自 `/api/bots`，若列表为空说明此刻确实没有被控端在线（浏览器只能看到在线机器）；
⑤ 终端页需要 WebSocket，若有反向代理请确保已放行 `Upgrade` 头。

**Q11：浏览器控制台需要登录吗？初始口令是什么？**
A：**需要登录**。首次启动会自动创建管理员 `admin`，**默认口令为 `admin123`**；
登录后可在「用户管理」面板新增账号、改口令、调整角色，也可修改 `admin` 自己的口令。
若删除了 `logs/helper.db`，重启后重新生成初始管理员（会清空已有账号与审计记录）。
控制台目前已启用鉴权但**仍不做 TLS**，请只在可信网络内使用。

**Q12：登录后打字没反应 / 看不到某台机器？**
A：两种情况都跟**角色与机器可见等级**有关：观察者（角色 1）是只读身份，终端可旁观但输入会被丢弃；
某台机器的 `min_role` 高于你的角色时，它对你就不可见、也无法操作。
只有**管理员**能在「机器管理」里调整机器等级、在「用户管理」里调整账号角色。

---

## 九、已知事项

- `helper/protocol/message.go` 与 `user/protocol/message.go` 是两份**内容相同的拷贝**，
  因两端为独立 module 而无法直接共享包。**修改协议时必须同步修改两处**。
- `helper/go.mod` 中 `gorilla/websocket` 被标记为 `// indirect`，属 Go 工具链的标注细节，
  不影响编译与运行。
- 项目当前**未包含开源许可证文件（LICENSE）**。若需对外分发或开源，请自行补充合适的许可证。
- 本项目**未实现传输加密**（无 TLS）；被控端接入的 `/ws` 也**不做身份认证**，
  仅适合学习与受信任网络内的实验，请勿直接用于生产环境。
- **浏览器控制台（WebUI）已支持登录鉴权与三级角色**（观察者 / 普通用户 / 管理员），
  账号、机器等级、会话与审计日志存入本地 SQLite（`logs/helper.db`）；
  但**仍不做 TLS**，与 `/ws` 共用一个明文端口，口令与 Cookie 在网络上明文传输。
- 会话令牌用自实现的 HS256 JWT 签名，签名密钥存于 `logs/helper.key`（权限 `0600`）；
  该文件与 `helper.db` 均为敏感文件，已加入 `.gitignore`，请勿泄露或提交到版本库。
- WebUI 前端（含 xterm.js）通过 `go:embed` 内嵌进 `helper.exe`，
  **新增/修改前端文件后必须重新编译**才会生效，不存在「改完刷新页面即可」的运行时加载。
- 同一台机器支持**多用户同时操作**（多个 DOS 窗口 / 多个浏览器页面）：非观察者各自拥有独立的
  操作上下文（独立 shell / 独立 cwd），输入互不干扰；观看 / 接续他人上下文需申请获同意；
  观察者只读旁观。

---

## 十、许可与免责

本项目为学习研究用途的个人项目，未声明开源许可证。

**再次郑重声明：本项目仅供学习研究使用，严禁用于任何非法用途。**
使用者须自行确保其行为符合所在国家 / 地区的法律法规，并对自己的一切行为承担全部责任。
