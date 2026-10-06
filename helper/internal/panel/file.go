package panel

// put / get：命令行面板的文件传输命令。与 Web 控制台共用同一条
// file_put / file_get 分片通道（core.WaitFileAck / core.FetchFileChunk），
// 只是没有 HTTP 与审计，直接在当前面板窗口展示进度。

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"remoteassist-helper/internal/core"
	"remoteassist-helper/protocol"
)

// handlePut put [botID] [本地文件] [可选远端目标路径]
//
// 把本地文件逐片投放给被控端；不填目标路径时落到被控端工作目录的 received/ 下。
// 逐片同步等 ack，天然限速；任一片失败即中断。
func handlePut(out io.Writer, m *core.Manager, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(out, "用法: put [botID] [本地文件] [可选远端目标路径]")
		return
	}
	botID, local := args[0], args[1]
	dest := ""
	if len(args) > 2 {
		dest = args[2]
	}

	b := m.Get(botID)
	if b == nil {
		fmt.Fprintf(out, "bot %s 不在线或不存在（用 bots 命令查看在线列表）\n", botID)
		return
	}

	f, err := os.Open(local)
	if err != nil {
		fmt.Fprintf(out, "打开本地文件失败: %v\n", err)
		return
	}
	defer f.Close()

	name := baseName(local)
	if dest == "" {
		dest = filepath.Join("received", name)
	}

	fileID := newFileID()
	ch, ok := m.RegisterFile(fileID, botID)
	if !ok {
		fmt.Fprintln(out, "传输会话冲突，请重试")
		return
	}
	defer m.UnregisterFile(fileID)

	chunkSize := protocol.DefaultFileChunkSize
	buf := make([]byte, chunkSize)
	var total int64
	for seq := 0; ; seq++ {
		n, rerr := io.ReadFull(f, buf)
		total += int64(n)
		last := false
		switch rerr {
		case nil:
			// 读满一片，未必是最后一片
		case io.EOF, io.ErrUnexpectedEOF:
			last = true
		default:
			fmt.Fprintf(out, "读取本地文件失败: %v\n", rerr)
			return
		}

		msg := &protocol.Message{
			Type:      protocol.TypeFilePut,
			FileID:    fileID,
			ChunkSeq:  seq,
			ChunkLast: last,
			Data:      protocol.EncodeB64(buf[:n]),
		}
		if seq == 0 {
			msg.Path = dest
			msg.ChunkSize = chunkSize
		}
		if err := b.Send(msg); err != nil {
			fmt.Fprintf(out, "发送到机器失败: %v\n", err)
			return
		}

		ack := core.WaitFileAck(ch)
		if ack == nil {
			fmt.Fprintln(out, core.ErrFileTimeout.Error())
			return
		}
		if ack.Err != "" {
			fmt.Fprintf(out, "被控端写入失败: %s\n", ack.Err)
			return
		}
		if last {
			fmt.Fprintf(out, "投放完成: %s（%s）→ %s\n", name, formatBytes(total), ack.Path)
			return
		}
	}
}

// handleGet get [botID] [远端路径] [可选本地保存路径]
//
// 从被控端逐片拉取指定文件写回本地；不填保存路径时落到当前目录的
// received/ 下（与投放落点对称）。任一片失败即中断。
func handleGet(out io.Writer, m *core.Manager, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(out, "用法: get [botID] [远端路径] [可选本地保存路径]")
		return
	}
	botID, src := args[0], args[1]
	local := ""
	if len(args) > 2 {
		local = args[2]
	}

	b := m.Get(botID)
	if b == nil {
		fmt.Fprintf(out, "bot %s 不在线或不存在（用 bots 命令查看在线列表）\n", botID)
		return
	}

	name := baseName(src)
	if local == "" {
		local = filepath.Join("received", name)
	}

	f, err := os.Create(local)
	if err != nil {
		fmt.Fprintf(out, "创建本地文件失败: %v\n", err)
		return
	}

	fileID := newFileID()
	ch, ok := m.RegisterFile(fileID, botID)
	if !ok {
		f.Close()
		fmt.Fprintln(out, "传输会话冲突，请重试")
		return
	}
	defer m.UnregisterFile(fileID)

	chunkSize := protocol.DefaultFileChunkSize
	var total int64

	first, err := core.FetchFileChunk(b, fileID, src, chunkSize, 0, ch)
	if err != nil {
		f.Close()
		fmt.Fprintf(out, "下载失败: %v\n", err)
		return
	}
	data, derr := protocol.DecodeB64(first.Data)
	if derr != nil {
		f.Close()
		fmt.Fprintln(out, "分片编码异常")
		return
	}
	if len(data) > 0 {
		total += int64(len(data))
		if _, werr := f.Write(data); werr != nil {
			f.Close()
			fmt.Fprintf(out, "写本地文件失败: %v\n", werr)
			return
		}
	}
	done := first.ChunkLast

	for seq := 1; !done; seq++ {
		chunk, err := core.FetchFileChunk(b, fileID, src, chunkSize, seq, ch)
		if err != nil {
			f.Close()
			fmt.Fprintf(out, "下载中断: %v\n", err)
			return
		}
		data, derr := protocol.DecodeB64(chunk.Data)
		if derr != nil {
			f.Close()
			fmt.Fprintln(out, "分片编码异常")
			return
		}
		if len(data) > 0 {
			total += int64(len(data))
			if _, werr := f.Write(data); werr != nil {
				f.Close()
				fmt.Fprintf(out, "写本地文件失败: %v\n", werr)
				return
			}
		}
		done = chunk.ChunkLast
	}

	if err := f.Close(); err != nil {
		fmt.Fprintf(out, "关闭本地文件失败: %v\n", err)
		return
	}
	fmt.Fprintf(out, "下载完成: %s（%s）→ %s\n", name, formatBytes(total), local)
}

// newFileID 生成一次文件传输会话的随机 ID
func newFileID() string {
	buf := make([]byte, 8)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// baseName 取路径的文件名部分（同时兼容 / 与 \ 分隔符）
func baseName(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	return filepath.Base(p)
}

// formatBytes 把字节数转成人类可读的带单位文本
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
