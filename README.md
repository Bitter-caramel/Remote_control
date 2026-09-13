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
│  前台：DOS 控制面板 (panel.go)                 │        │                                   │
│  后台：心跳检测 (heartbeat.go) / 监听          │        │                                   │
└──────────────────────────────────────────────┘        └───────────────────────────────────┘
```

### 2.2 关键设计

- **面向对象 + 一机一协程**：每台被控机器对应一个 `BOT` 对象，由 `listener.go` 中的一个独立
  读循环协程服务；协程之间不互相通信，只通过 `Manager` 的加锁容器协调，简化并发管理。
- **两级容器管理**：`Manager` 持有 `bots`（在线机器）与 `buffered`（疑似掉线、缓存等待的机器）
  两个列表。心跳丢失先进缓存区，连续 3 个周期未恢复才真正销毁协程、标记下线。
- **两端协议完全对称**：`helper/protocol/message.go` 与 `user/protocol/message.go` 是**内容一致**
  的两份拷贝（因两端是独立 module，无法直接共用）。**修改协议时必须同步改两处**，否则会握手失败。
- **二进制安全的终端通道**：终端是原始字节流（含 ANSI 控制序列，且可能在 UTF-8 多字节字符中间分包），
  因此 `input` / `output` 消息的 `data` 字段统一用 **Base64** 承载，避免 JSON 编码破坏二进制数据。
- **本地 IPC 独立于远程连接**：操控端弹出的「终端窗口」是**另一个 helper.exe 进程**（`-attach` 模式），
  它只连本机 `127.0.0.1` 的随机端口，由主进程桥接到对应机器的远程 WebSocket。
  好处是主面板不被终端输入输出占用，可同时开多个窗口管理多台机器。
- **平台相关代码隔离**：两端都用 `_windows.go` / `_unix.go` / `_other.go` 后缀 + `//go:build` 构建约束
  隔离平台差异。Windows 用 ConPTY、Linux 用 `/dev/ptmx` 伪终端（PTY），功能完整且对等；
  macOS 等其它平台仍可编译（降级为管道 shell、内嵌终端），便于开发自测。

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

- 优先使用 **ConPTY 伪终端**启动 `cmd.exe /K chcp 65001>nul`，初始工作目录为**用户主目录**
  （不是 `user.exe` 所在目录），默认尺寸 120×30；
- 需要 **Windows 10 1809 / Server 2019 或更新版本**；老系统自动降级为「常驻 `cmd.exe` + 管道」，
  目录与环境变量仍可保持，但少数直接读控制台的交互式程序不可用；
- Shell **惰性启动**（首次收到输入才创建）、**退出后自动重启**（例如远端敲了 `exit`）；
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
- **同一台机器同时只允许开一个终端窗口**（用 CAS 独占终端窗口位，防止多窗口争抢输入）；
- 机器下线时窗口会提示并等待回车，不会闪退；
- 不支持弹窗的平台（或弹窗失败）自动降级为当前窗口内的**内嵌终端**（按行模式，输入 `/quit` 退出）。

**本地 IPC 的安全设计（`attachserver.go`）**

- 只监听 `127.0.0.1`（外部网络不可达），端口随机分配（`127.0.0.1:0`）；
- 接入必须携带**每次启动重新生成的 16 字节随机令牌**；
- 校验 WebSocket `Origin`：非浏览器客户端（不带 Origin）放行，浏览器场景只放行本机回环来源；
- 接入请求必须在 **5 秒内**作为第一条消息发出，否则断开。

---

## 三、目录结构

```
Remote_control/
├── README.md                     # 本文件：项目总览、编译、配置、使用说明与免责声明
├── .gitignore                    # 忽略 bin/、config.json、*.log、agent_endpoint.json
├── helper.md / user.md           # 早期设计笔记
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
│   ├── internal/app/             # 全部业务逻辑（package app）
│   │   ├── main.go               #   启动流程：-attach/-cli 分流 → 日志 → 监听 → 控制面板
│   │   ├── bot.go                #   BOT 对象：ID/名称/系统/IP/Port/心跳/输出队列/专属日志
│   │   ├── manager.go            #   bots 管理器：在线列表 + 缓存区列表、botID 分配、上下线
│   │   ├── listener.go           #   TCP 监听 → WebSocket 升级；注册握手（同步绑定，失败不闪退）
│   │   ├── heartbeat.go          #   心跳检测后台协程（每 10s 扫描一次）
│   │   ├── panel.go / panel_bot.go       # 主控制面板 / 内嵌终端降级
│   │   ├── attachserver.go       #   本地 IPC：终端窗口接入、令牌鉴权、输入输出桥接
│   │   ├── cliserver.go          #   本地 IPC 的 /cli 端点：Agent 查询/执行通道
│   │   ├── agentcli.go           #   -cli 模式：list/exec 机器可读命令行
│   │   ├── execwait.go           #   exec 待回包注册表 + bot 摘要信息
│   │   ├── attach_client.go      #   -attach 模式：独立终端窗口进程
│   │   ├── attach_window_*.go    #   Windows 弹窗实现 / 非 Windows 降级
│   │   ├── console_*.go          #   Windows VT raw 模式 / 非 Windows 空实现
│   │   └── e2e_test.go           #   端到端回归测试（桥接/鉴权/CLI/互斥/下线/name-os 透传）
│   ├── protocol/message.go       # 两端共用的消息协议（JSON + Base64 字节流）
│   └── logx/                     # programlog + botslog + 每 bot 的 botlog（终端录像）
│
└── user/                         # ── 被控端（用户端 / Client）独立 Go module ──
    ├── go.mod / go.sum           # module remoteassist-user
    ├── cmd/user/main.go          # 薄入口：仅调用 internal/agent.Run()
    ├── internal/agent/           # 全部业务逻辑（package agent）
    │   ├── main.go               #   读/建 config → 主循环（连接 → 注册 → shell → 心跳）
    │   ├── config.go             #   config.json：server_addr / user_id / name（自定义名称）
    │   ├── conn.go               #   Dial、注册（上报系统与名称）、读循环、Ctrl+C 下线
    │   ├── sysinfo*.go           #   系统探测：Windows 版本号 / Linux /etc/os-release + 内核
    │   ├── agentexec.go          #   Agent 一次性命令执行（独立 shell，不碰交互 PTY）
    │   ├── shell.go / shell_windows.go / shell_other.go  # ConPTY 常驻终端与降级实现
    │   ├── heartbeat.go          #   周期心跳
    │   └── console_*.go          #   UTF-8 代码页
    └── protocol/message.go       # 与操控端一致的消息协议（两份拷贝，需同步修改）
```

**运行期自动生成的目录与文件**（均不入库）

```
helper 进程的当前工作目录/
├── logs/
│   ├── program.log               # 程序运行日志（INFO + ERROR）
│   ├── agent_endpoint.json       # 本地 CLI/Agent 接入点（地址 + 随机令牌），退出时删除
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

操控端内置端到端回归测试，覆盖「注册握手 → 本地 IPC 终端桥接与令牌鉴权 → 单机单窗互斥 →
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
| `HeartbeatInterval` | `helper/protocol/message.go` | `5 * time.Second` | **被控端**发送心跳的间隔 |
| `HeartbeatPeriod` | `helper/protocol/message.go` | `10 * time.Second` | **操控端**心跳检测周期 |
| `MaxMissed` | `helper/protocol/message.go` | `3` | 允许连续丢失的周期数，超过即下线 |

改完需**重新编译**。心跳相关常量若修改，必须**同步修改** `user/protocol/message.go` 中的同名常量。

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
| `defaultServerAddr` | `user/internal/agent/config.go` | `"127.0.0.1:8080"` | 首次生成 `config.json` 时写入的默认操控端地址；**发布前应改为实际部署地址** |
| `configPath` | `user/internal/agent/config.go` | `"config.json"` | 配置文件路径（相对进程工作目录） |
| `retryInterval` | `user/internal/agent/main.go` | `5 * time.Second` | 断线后的自动重连间隔 |
| ConPTY 初始尺寸 | `user/internal/agent/shell_windows.go` | `120 x 30` | 伪终端默认行列数，接入终端窗口后会被实际窗口尺寸覆盖 |

---

## 六、使用流程

### 6.1 快速开始（五步）

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

出现 `assist> ` 提示符即表示程序已在后台监听 `8080` 端口。

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
| `bot [botID]` | 为该机器弹出独立终端窗口（`Ctrl+]` 关闭；同机同时只允许一个窗口） |
| `exit` | 退出程序（不再监听端口，直至下次启动） |

### 6.3 被控端退出与重连

- **普通模式**：直接关闭窗口，或按 `Ctrl+C`（程序会先向操控端发送 `bye` 下线通知再退出）。
- **隐藏窗口模式**（`-H windowsgui` 编译）：打开任务管理器结束 `user.exe` 进程。
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
5. **传输是明文的**：两端通过 **WebSocket 明文**传输命令与回显，**没有任何加密与认证机制**
   （`CheckOrigin` 放行任意来源，被控端凭 `userID` 自称身份）。
   因此**只应在受信任的内网或 VPN / 加密隧道中使用**，切勿在公网无保护地暴露 8080 端口。
6. **及时终止**：协助结束后立即关闭 `user.exe`（任务管理器结束进程），不要长期后台常驻。
7. **审查与留痕**：操控端会完整记录所有命令与回显（`botlogs/<botID>.log`）。
   被控方有权要求操控方提供操作日志，或要求其在协助结束后清除相关日志。
8. **本机 IPC 相对安全，但不等于零风险**：终端窗口 IPC 只监听 `127.0.0.1`、端口随机、
   令牌每次启动重新生成，本机其它程序无法随意接入；但**本机其它进程若已具备调试 / 注入能力，仍可能被滥用**。
9. **本程序不做任何隐蔽驻留**：不写注册表、不安装服务、不设开机自启、不留后门。
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

**Q4：`bot [ID]` 提示「终端窗口已经打开了」？**
A：同一个 `botID` 同时只允许一个终端窗口（避免输入争抢）。请先关闭原窗口，
或在原窗口按 `Ctrl+]` 脱离后重试。

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

---

## 九、已知事项

- `helper/protocol/message.go` 与 `user/protocol/message.go` 是两份**内容相同的拷贝**，
  因两端为独立 module 而无法直接共享包。**修改协议时必须同步修改两处**。
- `helper/go.mod` 中 `gorilla/websocket` 被标记为 `// indirect`，属 Go 工具链的标注细节，
  不影响编译与运行。
- 项目当前**未包含开源许可证文件（LICENSE）**。若需对外分发或开源，请自行补充合适的许可证。
- 本项目**未实现加密与身份认证**，仅适合学习与受信任网络内的实验，请勿直接用于生产环境。

---

## 十、许可与免责

本项目为学习研究用途的个人项目，未声明开源许可证。

**再次郑重声明：本项目仅供学习研究使用，严禁用于任何非法用途。**
使用者须自行确保其行为符合所在国家 / 地区的法律法规，并对自己的一切行为承担全部责任。
