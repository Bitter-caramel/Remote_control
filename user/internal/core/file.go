package core

// 文件传输（被控端侧）：
//   - 投放：helper 逐片下发 file_put，本机把字节流式写入磁盘，末片回 ack 并带上最终写入路径；
//   - 下载：helper 逐片索要 file_get，本机从磁盘读一片回一片，直到文件末尾。
//
// 所有 file_put / file_get 都在 ReadLoop 的单一协程内串行处理，
// filePuts / fileGets 这两个 map 只被该协程访问，因此无需加锁。
// 会话是进程级的：断线（ReadLoop 退出）时统一关闭所有句柄；重连后旧会话作废，
// helper 侧会因收不到 ack/chunk 而超时失败，不会串到新连接。

import (
	"io"
	"os"
	"path/filepath"

	"remoteassist-user/internal/term"
	"remoteassist-user/protocol"
)

// putSession 投放接收会话（helper → 本机写文件）
type putSession struct {
	f    *os.File
	path string
}

// getSession 下载发送会话（本机读文件 → helper）
type getSession struct {
	f *os.File
}

// resolveXferPath 把协议下发的路径解析成绝对路径：
// 绝对路径原样使用；相对路径按被控端工作目录（用户主目录）解析。
func resolveXferPath(p string) string {
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(term.HomeDir(), p)
}

// handleFilePut 处理投放的一片数据。
// 首片（ChunkSeq=0）带目标路径，据此创建文件；后续片续写；末片（ChunkLast）关闭文件并回最终路径。
func (c *Client) handleFilePut(msg protocol.Message) {
	data, err := protocol.DecodeB64(msg.Data)
	if err != nil {
		c.replyFilePutAck(msg.FileID, msg.ChunkSeq, "", "分片编码异常")
		return
	}

	var sess *putSession
	if msg.ChunkSeq == 0 {
		path := resolveXferPath(msg.Path)
		if path == "" {
			c.replyFilePutAck(msg.FileID, msg.ChunkSeq, "", "缺少目标路径")
			return
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			c.replyFilePutAck(msg.FileID, msg.ChunkSeq, "", "创建目录失败: "+err.Error())
			return
		}
		f, err := os.Create(path)
		if err != nil {
			c.replyFilePutAck(msg.FileID, msg.ChunkSeq, "", "创建文件失败: "+err.Error())
			return
		}
		sess = &putSession{f: f, path: path}
		c.filePuts[msg.FileID] = sess
	} else {
		sess = c.filePuts[msg.FileID]
		if sess == nil {
			c.replyFilePutAck(msg.FileID, msg.ChunkSeq, "", "未知的投放会话")
			return
		}
	}

	if len(data) > 0 {
		if _, err := sess.f.Write(data); err != nil {
			delete(c.filePuts, msg.FileID)
			_ = sess.f.Close()
			c.replyFilePutAck(msg.FileID, msg.ChunkSeq, "", "写入失败: "+err.Error())
			return
		}
	}

	if msg.ChunkLast {
		path := sess.path
		if err := sess.f.Close(); err != nil {
			delete(c.filePuts, msg.FileID)
			c.replyFilePutAck(msg.FileID, msg.ChunkSeq, "", "关闭文件失败: "+err.Error())
			return
		}
		delete(c.filePuts, msg.FileID)
		c.replyFilePutAck(msg.FileID, msg.ChunkSeq, path, "")
		return
	}
	c.replyFilePutAck(msg.FileID, msg.ChunkSeq, "", "")
}

func (c *Client) replyFilePutAck(fileID string, seq int, path, errMsg string) {
	_ = c.Send(&protocol.Message{
		Type:     protocol.TypeFilePutAck,
		FileID:   fileID,
		ChunkSeq: seq,
		Path:     path,
		Err:      errMsg,
	})
}

// handleFileGet 处理下载请求：读一片回一片；读不到字节视为文件结束（ChunkLast）。
func (c *Client) handleFileGet(msg protocol.Message) {
	chunkSize := msg.ChunkSize
	if chunkSize <= 0 {
		chunkSize = protocol.DefaultFileChunkSize
	}

	var sess *getSession
	if msg.ChunkSeq == 0 {
		path := resolveXferPath(msg.Path)
		if path == "" {
			c.replyFileGetChunk(msg.FileID, 0, nil, true, "缺少源路径")
			return
		}
		f, err := os.Open(path)
		if err != nil {
			c.replyFileGetChunk(msg.FileID, 0, nil, true, "打开文件失败: "+err.Error())
			return
		}
		sess = &getSession{f: f}
		c.fileGets[msg.FileID] = sess
	} else {
		sess = c.fileGets[msg.FileID]
		if sess == nil {
			c.replyFileGetChunk(msg.FileID, msg.ChunkSeq, nil, true, "未知的下载会话")
			return
		}
	}

	buf := make([]byte, chunkSize)
	n, err := sess.f.Read(buf)
	if err != nil && err != io.EOF {
		delete(c.fileGets, msg.FileID)
		_ = sess.f.Close()
		c.replyFileGetChunk(msg.FileID, msg.ChunkSeq, nil, true, "读取失败: "+err.Error())
		return
	}
	if n == 0 {
		// 读到文件末尾：回空片并结束
		delete(c.fileGets, msg.FileID)
		_ = sess.f.Close()
		c.replyFileGetChunk(msg.FileID, msg.ChunkSeq, nil, true, "")
		return
	}
	last := n < chunkSize // 读不满一片，说明这是最后一片
	if last {
		delete(c.fileGets, msg.FileID)
		_ = sess.f.Close()
	}
	c.replyFileGetChunk(msg.FileID, msg.ChunkSeq, buf[:n], last, "")
}

func (c *Client) replyFileGetChunk(fileID string, seq int, data []byte, last bool, errMsg string) {
	_ = c.Send(&protocol.Message{
		Type:      protocol.TypeFileGetChunk,
		FileID:    fileID,
		ChunkSeq:  seq,
		Data:      protocol.EncodeB64(data),
		ChunkLast: last,
		Err:       errMsg,
	})
}

// closeFileSessions 断线时关闭所有文件句柄，防止泄漏
func (c *Client) closeFileSessions() {
	for id, s := range c.filePuts {
		_ = s.f.Close()
		delete(c.filePuts, id)
	}
	for id, s := range c.fileGets {
		_ = s.f.Close()
		delete(c.fileGets, id)
	}
}