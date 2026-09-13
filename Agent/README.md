# Agent 指南：用命令行管理和操作远程 bot

本目录的文档面向 **AI 编码/运维 Agent**（以及写自动化脚本的人类）。
读完本文档，你应该能够仅通过运行 `helper.exe` 的 CLI 子命令，完成：

- 查询当前有哪些被控机（bot）在线；
- 在指定 bot 上执行任意一条命令，拿到输出和退出码；
- 把多步诊断、巡检、处置流程脚本化。

> 法律与授权前提：本工具仅限**已获得明确授权**的设备。未经授权控制他人计算机可能触犯
> 《中华人民共和国刑法》第 285 条。执行任何非只读操作前，先阅读文末
> [安全与授权红线](#六安全与授权红线agent-必须遵守)。

---

## 一、架构速览（30 秒）

```
 helper.exe（主程序，常驻）                 user.exe（被控端，常驻在目标机）
 ├─ :8080 远程 WebSocket  ◀───────────────  注册/心跳/执行结果
 ├─ 127.0.0.1:随机端口 本地 IPC
 │    ├─ /attach  人工终端窗口（bot 弹窗，交互式 PTY）
 │    └─ /cli     ◀── helper.exe -cli ...   ← 你（Agent）走这里
 └─ logs/
      ├─ agent_endpoint.json   本地 IPC 地址+令牌（CLI 自动发现）
      ├─ bots.log              所有上线过的 bot
      └─ botlogs/<botID>.log   该 bot 的完整审计（含 [AGENT] 行）
```

关键点：

1. **必须先有一个正在运行的 helper.exe 主程序**，CLI 只是它的本地客户端，自己不监听 8080。
2. CLI 执行的是**一次性命令**：每次在被控机上新开一个短命 shell 进程执行、收集完整输出后退出。
   它**不占用、也不干扰**人工打开的交互终端窗口，不改变交互 shell 的当前目录。
3. 本地 IPC 只绑定 `127.0.0.1`，每次启动生成随机令牌；CLI 凭令牌接入。

---

## 二、前置检查清单

按顺序确认（以下路径相对仓库根目录）：

1. 有编译好的程序：`bin/helper.exe`、`bin/user.exe`
   （没有就双击 `scripts/build-helper-windows.bat` 与 `scripts/build-user-windows.bat`，需要 Go 1.21+）。
2. helper 主程序已启动并常驻（人工启动或后台启动均可）。
3. 至少一台目标机运行着 user.exe 并注册成功。
4. 你的 CLI 与 helper 主程序在**同一台机器、同一用户**下运行（自动查找 `logs/agent_endpoint.json`）。

最小启动方式（PowerShell，测试用）：

```powershell
# 终端 A：启动主程序（保持运行；生产环境由人来启动）
.\bin\helper.exe

# 终端 B（或目标机）：启动被控端
.\bin\user.exe
```

> 端口冲突时可用环境变量 `RA_LISTEN=:18080` 改 helper 监听端口；
> 此时被控端需把运行目录下 `config.json` 的 `server_addr` 改成 `127.0.0.1:18080`。

---

## 三、快速上手

PowerShell：

```powershell
# 也可用全路径：D:\...\Remote_control\bin\helper.exe
$helper = ".\bin\helper.exe"

# 1) 谁在线？（程序可解析的 JSON）
& $helper -cli list --json
# [
#   {
#     "id": "Bf99c224c",
#     "name": "财务室-电脑",
#     "os": "windows/amd64 (10.0.26200)",
#     "addr": "192.168.1.20:50231",
#     "buffered": false
#   }
# ]

# 2) 在该 bot 上执行命令（推荐永远带 --json，便于稳定解析）
& $helper -cli exec Bf99c224c 'whoami' --json
# {
#   "bot": "Bf99c224c",
#   "exit_code": 0,
#   "output": "desktop-xxxx\\elysia\r\n"
# }

# 3) 多步巡检示例（可以按名称挑选，也可以直接按 id）
$bot = (& $helper -cli list --json | ConvertFrom-Json |
        Where-Object { -not $_.buffered } | Select-Object -First 1).id
& $helper -cli exec $bot 'hostname'
& $helper -cli exec $bot 'ipconfig'
& $helper -cli exec $bot 'netstat -ano | findstr ESTABLISHED'
```

cmd.exe：

```cmd
bin\helper.exe -cli list --json
bin\helper.exe -cli exec Bf99c224c "ipconfig /all" --json
```

---

## 四、CLI 参考手册

通用形式：

```
helper.exe -cli list [--json]
helper.exe -cli exec <botID> <命令> [--timeout 秒] [--json]
```

### 4.1 `list` — 在线机器列表

| 输出模式 | 内容 |
|---|---|
| 默认（表格） | `botID / 名称 / 系统 / 地址 / 状态`，无机器时打印"（当前没有在线机器）" |
| `--json` | JSON 数组：`[{"id":"B...","name":"财务室-电脑","os":"windows/amd64 (10.0.26200)","addr":"ip:port","buffered":false}]` |

字段说明：`name` 是被控机在 config.json 自填的名称（空串=未设置）；`os` 是被控机系统描述
（Windows 为 `windows/amd64 (主.次.构建号)`，Linux 为 `linux/amd64 (发行版, 内核版本)`）。
`buffered=true` 表示心跳异常、正在等待确认（疑似掉线），**不要**对其下发处置类命令。

### 4.2 `exec` — 执行一条命令

| 参数 | 说明 |
|---|---|
| `<botID>` | list 返回的 ID |
| `<命令>` | **整条命令作为一个参数**传入（PowerShell 用单引号，cmd 用双引号）；命令内部需要的引号自己写，例如 `'dir "C:\Program Files"'` |
| `--timeout N` | 超时秒数，默认 **120**，最大 **600**；超时后被控端终止该进程，返回 `exit_code=-1` |
| `--json` | 返回结构化结果（Agent 推荐始终使用） |

纯文本模式：原样打印命令输出；进程退出码**透传为 helper.exe 的退出码**。

JSON 模式响应：

```json
{
  "bot": "Bf99c224c",
  "exit_code": 0,
  "output": "stdout+stderr 合并内容，UTF-8，保留原始换行(\\r\\n)"
}
```

错误响应（本地/协议级错误，一律输出到 stderr 或 JSON error，退出码 **2**）：

```json
{ "error": "机器不在线: Bdead" }
```

### 4.3 退出码约定（Agent 判断成败的依据）

| 退出码 | 含义 |
|---|---|
| `0` | 调用成功，远端命令退出码为 0 |
| `1..255` | 调用本身成功，**退出码是远端命令的退出码**（非零代表命令失败，不是 CLI 故障） |
| `2` | 本地/协议错误：找不到 helper、鉴权失败、bot 不在线、超时（`exit_code=-1`）、参数错误等 |

### 4.4 接入点发现与覆盖（一般不需要手动管）

CLI 按以下优先级找到正在运行的 helper：

1. `--addr 127.0.0.1:端口 --token xxx` 参数；
2. 环境变量 `RA_ADDR` / `RA_TOKEN`；
3. 自动查找 `agent_endpoint.json`：从**当前目录**和 **helper.exe 所在目录（如 `bin/`）**
   各自向上最多 5 级，依次检查 `<dir>/agent_endpoint.json`、`<dir>/logs/agent_endpoint.json`、
   `<dir>/helper/logs/agent_endpoint.json`（仓库布局 `bin/` 与 `helper/` 同级时自动命中）。

helper 主程序退出时会自动删除该文件。**文件里的 token 等同于本地操作能力，不要打印、外传或提交到 git。**

### 4.5 输出编码

被控端执行时先切 `chcp 65001`；输出统一按 UTF-8 返回。
在简体中文系统上，对无视代码页的老程序会自动回退 GBK 解码。stdout 与 stderr 合并返回。

---

## 五、语义与限制（踩坑前必读）

1. **无 shell 状态保持。** 每条命令都是全新进程，初始目录是被控端用户主目录。
   想在指定目录连续操作，写在**同一条**命令里：

   ```powershell
   .\helper.exe -cli exec $bot 'cd /d D:\app\logs && dir'
   ```

2. **不支持交互式程序。** 需要 TTY/键盘交互的（`python` REPL、`diskpart`、`netsh` 交互、
   `set /p`、vim 等）不能走 CLI。改用非交互形式（`python script.py`、`netsh advfirewall show ...`）；
   确实需要人工交互的场景，请让人在主面板 `bot <ID>` 弹出的终端窗口里操作。

3. **不要用来跑长驻服务。** 超过 timeout 的进程会被杀。启停服务用 `sc`/`net start` 这类立即返回的命令。

4. **命令注入就是设计本身。** `<命令>` 原样交给被控端 `cmd /c` 执行，没有转义层——
   所以永远不要把不可信输入拼进命令字符串。

5. **与人工操作隔离。** CLI 命令在独立进程执行，正在用 `bot` 窗口操作的人看不到也不会被打断；
   但你们改的是同一台机器，避免并发做互相冲突的变更（同时装软件、同时改同一配置）。

6. **幂等与可重跑。** 写处置脚本时优先选择可重复执行不出错的写法
   （如 `if not exist` 判断、`sc query` 后再动作）。

7. **Windows 防火墙/杀软**可能弹窗拦截 user.exe，这由被控机本地策略决定，CLI 无法绕过。

---

## 六、安全与授权红线（Agent 必须遵守）

1. **授权确认**：只操作用户明确指认的 botID/机器。list 里出现不等于可以动手。
2. **只读先行**：首次排查先跑只读命令（`hostname`、`ipconfig`、`tasklist`、`netstat`、
   `sc query`、事件日志查询），拿出现场再提方案。
3. **高危动作二次确认**：以下操作**必须先向用户说明影响并获得确认**后再执行：
   - 删除/覆盖/批量修改文件、清空日志；
   - 改注册表、服务、启动项、计划任务；
   - 关防火墙/Defender、加账号、改权限、改网络配置；
   - 安装/卸载软件、打补丁、重启/关机；
   - 任何外发数据（上传、下载到外部地址）。
4. **最小影响**：优先原生命令，不往被控机投放自己的 exe/脚本，确需投放时先说明、用完清理。
5. **全程留痕**：每条 CLI 命令和输出（含退出码）都会写进
   `logs/botlogs/<botID>.log` 的 `[AGENT] CMD >` / `[AGENT] OUT <` 行。不要试图删除或篡改审计日志。
6. **令牌保密**：不回显 token，不把 `agent_endpoint.json` 内容写进对话、日志或提交。

---

## 七、推荐的 Agent 工作流

```
1. list --json 拿在线列表
   └─ 空 → 报告"没有在线机器"，不要尝试连接 8080 自己发协议
2. 选定 botID（buffered=false）
3. 只读诊断：3~8 条短命令收集信息（系统版本/网络/进程/服务/日志）
   └─ 每条解析 JSON 的 exit_code：2=调用故障，其他非 0=命令结果失败
4. 汇总结论 → 给出处置方案 → 高危动作等用户确认
5. 执行处置 → 用只读命令复验效果
6. 报告：做了什么、证据输出、遗留风险
```

稳健调用片段（PowerShell，超时与失败处理）：

```powershell
function Invoke-BotCmd($bot, $cmd, $timeout = 120) {
    $raw = & helper.exe -cli exec $bot $cmd --timeout $timeout --json 2>$null
    if ($LASTEXITCODE -eq 2) { throw "CLI 调用失败: $($raw | Out-String)" }
    $raw | ConvertFrom-Json   # .exit_code 是远端退出码，.output 是输出
}
```

---

## 八、故障排查

| 现象 | 原因与处理 |
|---|---|
| `找不到正在运行的 helper` | 主程序没启动；或当前目录/exe 目录都找不到 `logs\agent_endpoint.json`。用 `--addr/--token` 指定（从 helper 机的 logs 目录取） |
| `鉴权失败` | token 不匹配（helper 重启后会换新令牌）。重新读取 `agent_endpoint.json` |
| `机器不在线: Bxxx` | list 确认 ID；buffered 状态等心跳恢复；被控端可能已退出 |
| `等待执行结果超时` | 命令超过 timeout，或执行期间机器下线。注意：超时只保证杀进程，不保证子进程树全部退出 |
| 输出乱码 | 老程序无视 `chcp 65001` 且系统非 936 代码页；在命令里显式 `chcp 65001 >nul & 你的命令` |
| list 有机器但 exec 不通 | 检查 helper 与 user 版本是否匹配（protocol 消息需同版本） |
| 端口 8080 起不来 | 被占用（日志会明确报 bind 错误）。用 `RA_LISTEN=:其他端口` 并同步改 user 配置 |

---

## 九、协议备注（想直接对接 IPC 时看）

CLI 本质是一个 WebSocket 客户端：`ws://127.0.0.1:<port>/cli`，
消息结构见 `helper/protocol/message.go`（user 端有一份完全相同的副本，改动必须两端同步）：

- 握手：发 `cli_hello{data: token}` → 收 `cli_hello_ack{data:"ok"}`；
- `cli_list` → `cli_list_ack{data: <BotInfo JSON 数组>}`；
- `cli_exec{userID, data: base64(命令), msgID, timeout}`
  → `cli_result{msgID, data: base64(输出), exitCode}` 或 `cli_error{msgID,data}`。

**绝大多数 Agent 不需要直接说协议，调用 `-cli --json` 即可。**
