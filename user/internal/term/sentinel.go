package term

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"runtime"
	"strings"
)

// sentinel.go：提示符哨兵。
//
// 裸 PTY 没有「命令执行结束」这个事件，而「一条命令跑完为一次操作」是观看者
// 同步的粒度，所以必须自己造一个信号：给 shell 的提示符末尾追加一段
// **不可见的 OSC 序列**（终端不渲染 OSC），并让它带上当前工作目录：
//
//	ESC ] 1337;RA;<nonce>:<cwd> ESC \
//
// 输出流里一出现这个序列，就说明上一条命令跑完、提示符重新出现了。
// nonce 每个上下文随机生成，避免误判用户自己打印的 OSC。
// 服务端两侧都看不到它——Pump 会先剥离再回传。
const (
	sentinelOSC = "\x1b]1337;RA;" // OSC 1337;RA;（RA = RemoteAssist）
	// sentinelMaxPending 未决字节上限：超过就认为不是哨兵，原样透传，
	// 避免用户打印了半截 OSC 导致我们无限缓存。
	sentinelMaxPending = 8192

	// oscBEL / oscST 两种 OSC 终结符
	oscBEL = 0x07
	oscESC = 0x1b
)

// newNonce 生成一个上下文的随机标识
func newNonce() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b)
}

// promptEnv 返回注入哨兵后提示符对应的环境变量名与取值。
//
// 只能通过**子进程环境变量**注入，绝不能往 shell 里写 `set PROMPT=` / `PS1=`
// 这类命令：行规程会把写入的字节回显出来，用户会在终端里看到一串乱码，
// 而且这段文本也不是真正的 OSC，解析器剥不掉。
func promptEnv(nonce string) (name, value string) {
	if runtime.GOOS == "windows" {
		// cmd 的 PROMPT 里 $E 表示 ESC；$P 是当前路径，$G 是 '>'。
		// 可见部分保持默认外观（路径 + '>'），哨兵追加在末尾。
		return "PROMPT", `$P$G` + `$E]` + "1337;RA;" + nonce + `:` + `$P` + `$E\`
	}
	// bash/zsh/sh 的 PS1：\[ \] 包住不可见部分，\w 是当前目录，\e 是 ESC。
	return "PS1", `\w \$ \[` + `\e]` + "1337;RA;" + nonce + `:\w` + `\e\\` + `\]`
}

// sentinelEvent 一次解析产出的事件。二者按流中出现的前后顺序排列：
//   - data 事件：可直接下发给终端的字节（哨兵已剥离）
//   - 段边界事件：上一条命令跑完（hit=true，cwd 为提示符当时的工作目录）
//
// 必须保序：一次读可能同时含「输出 + 哨兵 + 输出 + 哨兵」，
// 若把输出合并后再统一报边界，段的归属就错了。
type sentinelEvent struct {
	data []byte
	hit  bool
	cwd  string
}

// sentinelParser 流式解析 shell 输出：剥离本上下文的哨兵，并报告每一段的结束。
// 不是本 nonce 的 OSC 一律原样透传（用户程序自己打印的序列不能吞掉）。
type sentinelParser struct {
	nonce   string
	pending []byte
}

// cloneB 复制字节切片：解析器复用 pending 的底层数组，
// 直接返回其子切片会在下一轮被覆写。
func cloneB(b []byte) []byte {
	c := make([]byte, len(b))
	copy(c, b)
	return c
}

// bodyOf 从 OSC 片段（以 ESC ] 开头、end 为终止符之后的下标）中取出 body。
// 终止符是 BEL（1 字节）或 ST（ESC \，2 字节）。
func bodyOf(rest []byte, end int) string {
	if end >= 1 && rest[end-1] == oscBEL {
		return string(rest[2 : end-1])
	}
	return string(rest[2 : end-2])
}

// feed 消化一段原始输出，按流顺序返回事件列表（哨兵已剥离，段边界已标注）。
func (p *sentinelParser) feed(in []byte) []sentinelEvent {
	p.pending = append(p.pending, in...)
	mine := "1337;RA;" + p.nonce + ":"
	var evs []sentinelEvent

	for {
		i := bytes.Index(p.pending, []byte("\x1b]"))
		if i < 0 {
			// 没有 OSC 起始；结尾若是孤立的 ESC 则留到下一批（可能是 "\x1b]" 被拆开）
			keep := 0
			if n := len(p.pending); n > 0 && p.pending[n-1] == oscESC {
				keep = 1
			}
			if n := len(p.pending) - keep; n > 0 {
				evs = append(evs, sentinelEvent{data: cloneB(p.pending[:n])})
			}
			p.pending = append(p.pending[:0], p.pending[len(p.pending)-keep:]...)
			return evs
		}

		if i > 0 {
			evs = append(evs, sentinelEvent{data: cloneB(p.pending[:i])})
		}
		rest := p.pending[i:]

		// 找 OSC 终止符：BEL 或 ST
		end := -1
		for j := 1; j < len(rest); j++ {
			if rest[j] == oscBEL {
				end = j + 1
				break
			}
			if rest[j] == oscESC && j+1 < len(rest) && rest[j+1] == '\\' {
				end = j + 2
				break
			}
		}
		if end < 0 {
			// 终止符还没到：先攒着；攒太多就判定不是哨兵，吐出起始两字节继续
			if len(rest) > sentinelMaxPending {
				evs = append(evs, sentinelEvent{data: cloneB(rest[:2])})
				p.pending = append(p.pending[:0], rest[2:]...)
				continue
			}
			p.pending = append(p.pending[:0], rest...)
			return evs
		}

		body := bodyOf(rest, end)
		if strings.HasPrefix(body, mine) {
			evs = append(evs, sentinelEvent{hit: true, cwd: strings.TrimPrefix(body, mine)})
		} else {
			evs = append(evs, sentinelEvent{data: cloneB(rest[:end])}) // 别人的 OSC，原样透传
		}
		p.pending = append(p.pending[:0], rest[end:]...)
	}
}
