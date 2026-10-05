package term

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 提示符哨兵是整个多上下文方案最脆弱的一环：它决定「一条命令跑完」这个段边界
// 是否成立，进而决定观看者能否按段同步。这里既测解析状态机的纯逻辑，
// 也起一个真实 shell 验证 cmd 的 $E / bash 的 PS1 注入确实可用且不被渲染。

func TestFeedStripsSentinelAndKeepsOrder(t *testing.T) {
	const nonce = "abcd1234"
	p := &sentinelParser{nonce: nonce}
	sent := "\x1b]1337;RA;" + nonce + ":C:\\work\x1b\\"

	// 一次读里含「输出 + 哨兵 + 输出 + 哨兵」，必须保序返回
	in := []byte("hello" + sent + "world" + sent)
	evs := p.feed(in)

	var got []string
	for _, ev := range evs {
		if ev.hit {
			got = append(got, "HIT:"+ev.cwd)
		} else {
			got = append(got, "DATA:"+string(ev.data))
		}
	}
	want := []string{"DATA:hello", "HIT:C:\\work", "DATA:world", "HIT:C:\\work"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("事件顺序/内容不符\n got=%v\nwant=%v", got, want)
	}

	// 可见字节里不允许残留哨兵
	var visible []byte
	for _, ev := range evs {
		visible = append(visible, ev.data...)
	}
	if bytes.Contains(visible, []byte("1337;RA;")) {
		t.Fatalf("哨兵泄漏到可见输出: %q", visible)
	}
}

func TestFeedKeepsForeignOSC(t *testing.T) {
	const nonce = "abcd1234"
	p := &sentinelParser{nonce: nonce}

	// 别人的 OSC（不同 nonce）必须原样透传，不能被吞掉
	foreign := "\x1b]0;window title\x07"
	evs := p.feed([]byte("a" + foreign + "b"))
	var visible []byte
	for _, ev := range evs {
		if ev.hit {
			t.Fatalf("外来 OSC 被误判为段边界: %q", ev.cwd)
		}
		visible = append(visible, ev.data...)
	}
	if string(visible) != "a"+foreign+"b" {
		t.Fatalf("外来 OSC 未原样透传: %q", visible)
	}
}

func TestFeedHandlesSplitAcrossReads(t *testing.T) {
	const nonce = "abcd1234"
	p := &sentinelParser{nonce: nonce}
	full := []byte("x\x1b]1337;RA;" + nonce + ":/tmp\x1b\\y")

	// 逐字节喂入：跨批被拆开的 ESC 与序列都必须正确拼回
	var visible []byte
	hits := 0
	for i := range full {
		for _, ev := range p.feed(full[i : i+1]) {
			if ev.hit {
				hits++
				if ev.cwd != "/tmp" {
					t.Fatalf("cwd 解析错误: %q", ev.cwd)
				}
			} else {
				visible = append(visible, ev.data...)
			}
		}
	}
	if hits != 1 {
		t.Fatalf("段边界数 = %d，期望 1", hits)
	}
	if string(visible) != "xy" {
		t.Fatalf("可见输出 = %q，期望 \"xy\"", visible)
	}
}

// shellNewline 交互式输入的换行：cmd 用 CRLF，Unix 用 LF
func shellNewline() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}

// TestSentinelOnRealShell 起一个真实 shell，验证：
//  1. 提示符注入生效（能命中哨兵并拿到 cwd）；
//  2. 哨兵对终端不可见（不出现在下发的可见字节里）；
//  3. 「一条命令跑完」确实产生一次且仅一次段边界。
func TestSentinelOnRealShell(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过真实 shell 集成测试（-short）")
	}

	nonce := newNonce()
	sh, err := startShell(nonce, HomeDir())
	if err != nil {
		t.Fatalf("启动 shell 失败: %v", err)
	}
	defer sh.Close()

	// 读端放独立协程：PTY 读会阻塞，需要在外层做超时
	out := make(chan []byte, 32)
	go func() {
		for {
			b := make([]byte, 4096)
			n, err := sh.Read(b)
			if n > 0 {
				out <- b[:n]
			}
			if err != nil {
				close(out)
				return
			}
		}
	}()

	p := &sentinelParser{nonce: nonce}
	var visible []byte
	var cwds []string

	// 等待注入后的第一次提示符
	timeout := time.After(20 * time.Second)
	for len(cwds) == 0 {
		select {
		case b, ok := <-out:
			if !ok {
				t.Fatalf("shell 提前退出；可见输出=%q", visible)
			}
			for _, ev := range p.feed(b) {
				if ev.hit {
					cwds = append(cwds, ev.cwd)
				} else {
					visible = append(visible, ev.data...)
				}
			}
		case <-timeout:
			t.Fatalf("20 秒内未命中哨兵；可见输出=%q", visible)
		}
	}
	// 用真实转义序列判定泄漏：命令回显里会出现字面量 "$E]1337;RA;..."，那不是泄漏
	if bytes.Contains(visible, []byte("\x1b]1337;RA;"+nonce)) {
		t.Fatalf("哨兵泄漏到可见输出: %q", visible)
	}
	if strings.TrimSpace(cwds[0]) == "" {
		t.Fatalf("哨兵未携带 cwd；可见输出=%q", visible)
	}
	t.Logf("注入生效，cwd=%q", cwds[0])

	// 跑一条命令：应产生恰好一个新段边界，且输出里能看到命令回显
	if _, err := sh.Write([]byte("echo RA_PING" + shellNewline())); err != nil {
		t.Fatalf("写入命令失败: %v", err)
	}
	visible = visible[:0]
	timeout = time.After(20 * time.Second)
	for len(cwds) < 2 {
		select {
		case b, ok := <-out:
			if !ok {
				t.Fatalf("shell 提前退出；可见输出=%q", visible)
			}
			for _, ev := range p.feed(b) {
				if ev.hit {
					cwds = append(cwds, ev.cwd)
				} else {
					visible = append(visible, ev.data...)
				}
			}
		case <-timeout:
			t.Fatalf("20 秒内未收到第二条命令的段边界；可见输出=%q", visible)
		}
	}
	if !bytes.Contains(visible, []byte("RA_PING")) {
		t.Fatalf("未看到命令输出；可见输出=%q", visible)
	}
	t.Logf("段边界正确，cwds=%v", cwds)
}
