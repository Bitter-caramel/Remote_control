// Package file 文件传输功能插件（第一个迁移成插件架构的功能，也是新功能的样板）。
//
// 传输走「浏览器 → helper(HTTP) → 被控端(WS)」链路，helper 在中间逐片转发：
//   - 投放：浏览器把文件字节流 POST 过来，helper 逐片下发 file_put，等被控端逐片
//     file_put_ack 确认（逐片同步，天然限速），末片 ack 带回被控端最终写入路径；
//   - 下载：helper 逐片向被控端索要 file_get，收 file_get_chunk 写回响应流。
//
// 权限：与「操作」一致，仅普通用户及以上（CanOperate）且满足机器等级门槛；
// 观察者只读，不能传文件。全程写审计。
//
// 注册：包内 init() 调 feat.RegisterFeature，webui/features.go 空白导入本包。
// 新增功能请照抄本包结构与 doc/新增控制台功能插件.md。
package file

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"remoteassist-helper/internal/auth"
	"remoteassist-helper/internal/core"
	"remoteassist-helper/internal/store"
	"remoteassist-helper/internal/webui/feat"
	"remoteassist-helper/protocol"
)

func init() {
	feat.RegisterFeature(feat.Manifest{
		ID:    "file",
		Name:  "文件传输",
		Layer: "L2",
		Order: 20,
		Mount: mount,
	})
}

// mount 把本功能的路由挂到 mux 上（带登录鉴权）
func mount(d feat.Deps, mux *http.ServeMux) {
	mux.Handle("POST /api/bots/{id}/file",
		d.Auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handlePut(d, w, r)
		})))
	mux.Handle("GET /api/bots/{id}/file",
		d.Auth.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleGet(d, w, r)
		})))
}

// fileChunkTimeout 等待被控端逐片反馈的超时；逐片同步，超时即中断
const fileChunkTimeout = 60 * time.Second

// errFileTimeout 被控端在规定时间内未回片（含机器下线）
var errFileTimeout = errors.New("等待被控端响应超时或机器已下线")

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

// waitFileAck 等待被控端回一片反馈；msg 为 nil 表示超时或机器下线
func waitFileAck(ch chan *protocol.Message) *protocol.Message {
	select {
	case msg := <-ch:
		return msg
	case <-time.After(fileChunkTimeout):
		return nil
	}
}

// handlePut POST /api/bots/{id}/file?name=<文件名>&path=<可选目标路径>
//
// 请求体为原始文件字节流；name 必填（默认落点用它拼成 received/<name>），
// path 可选（操作者手填的绝对/相对目标路径，覆盖默认落点）。
func handlePut(d feat.Deps, w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	botID := r.PathValue("id")
	if !id.CanOperate() {
		feat.WriteError(w, http.StatusForbidden, "观察者无法投放文件")
		return
	}
	if id.Role < d.St.MinRoleOf(botID) {
		feat.WriteError(w, http.StatusForbidden, "无权访问该机器")
		return
	}
	b := d.M.Get(botID)
	if b == nil {
		feat.WriteError(w, http.StatusNotFound, "机器不在线: "+botID)
		return
	}

	q := r.URL.Query()
	name := baseName(strings.TrimSpace(q.Get("name")))
	if name == "" || name == "." {
		feat.WriteError(w, http.StatusBadRequest, "缺少文件名")
		return
	}
	dest := strings.TrimSpace(q.Get("path"))
	if dest == "" {
		dest = filepath.Join("received", name) // 相对路径：被控端落到 <工作目录>/received/
	}

	fileID := newFileID()
	ch, ok := d.M.RegisterFile(fileID, botID)
	if !ok {
		feat.WriteError(w, http.StatusInternalServerError, "传输会话冲突，请重试")
		return
	}
	defer d.M.UnregisterFile(fileID)

	chunkSize := protocol.DefaultFileChunkSize
	buf := make([]byte, chunkSize)
	var total int64
	for seq := 0; ; seq++ {
		n, err := io.ReadFull(r.Body, buf)
		total += int64(n)
		last := false
		switch err {
		case nil:
			// 读满一片，未必是最后一片
		case io.EOF, io.ErrUnexpectedEOF:
			last = true
		default:
			feat.WriteError(w, http.StatusBadRequest, "读取上传失败: "+err.Error())
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
			feat.WriteError(w, http.StatusBadGateway, "发送到机器失败: "+err.Error())
			return
		}

		ack := waitFileAck(ch)
		if ack == nil {
			feat.WriteError(w, http.StatusGatewayTimeout, errFileTimeout.Error())
			return
		}
		if ack.Err != "" {
			feat.WriteError(w, http.StatusBadGateway, "被控端写入失败: "+ack.Err)
			return
		}
		if last {
			d.Audit(id, r, store.ActionFilePut, botID,
				"投放 "+name+" → "+ack.Path+"（"+formatBytes(total)+"）")
			feat.WriteJSON(w, map[string]any{
				"status": "ok",
				"path":   ack.Path,
				"size":   total,
			})
			return
		}
	}
}

// handleGet GET /api/bots/{id}/file?path=<源路径>
//
// 把被控端指定路径的文件逐片拉回，流式写进响应（浏览器据此下载保存）。
func handleGet(d feat.Deps, w http.ResponseWriter, r *http.Request) {
	id, _ := auth.FromContext(r.Context())
	botID := r.PathValue("id")
	if !id.CanOperate() {
		feat.WriteError(w, http.StatusForbidden, "观察者无法下载文件")
		return
	}
	if id.Role < d.St.MinRoleOf(botID) {
		feat.WriteError(w, http.StatusForbidden, "无权访问该机器")
		return
	}
	b := d.M.Get(botID)
	if b == nil {
		feat.WriteError(w, http.StatusNotFound, "机器不在线: "+botID)
		return
	}

	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		feat.WriteError(w, http.StatusBadRequest, "缺少源文件路径")
		return
	}

	fileID := newFileID()
	ch, ok := d.M.RegisterFile(fileID, botID)
	if !ok {
		feat.WriteError(w, http.StatusInternalServerError, "传输会话冲突，请重试")
		return
	}
	defer d.M.UnregisterFile(fileID)

	chunkSize := protocol.DefaultFileChunkSize
	var total int64

	// 先取第一片，成功拿到数据后才写响应头，好让「文件不存在」等错误
	// 仍能以 JSON 返回，而不是污染已开始的下载流。
	first, err := fetchFileChunk(b, fileID, path, chunkSize, 0, ch)
	if err != nil {
		feat.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+baseName(path)+`"`)

	data, derr := protocol.DecodeB64(first.Data)
	if derr != nil {
		feat.WriteError(w, http.StatusBadGateway, "分片编码异常")
		return
	}
	if len(data) > 0 {
		total += int64(len(data))
		if _, werr := w.Write(data); werr != nil {
			return
		}
	}
	done := first.ChunkLast

	for seq := 1; !done; seq++ {
		chunk, err := fetchFileChunk(b, fileID, path, chunkSize, seq, ch)
		if err != nil {
			return // 响应头已发，无法再回 JSON，直接中断流
		}
		data, derr := protocol.DecodeB64(chunk.Data)
		if derr != nil {
			return
		}
		if len(data) > 0 {
			total += int64(len(data))
			if _, werr := w.Write(data); werr != nil {
				return
			}
		}
		done = chunk.ChunkLast
	}

	d.Audit(id, r, store.ActionFileGet, botID,
		"下载 "+path+"（"+formatBytes(total)+"）")
}

// fetchFileChunk 发一次 file_get 请求并等回对应片
func fetchFileChunk(b *core.BOT, fileID, path string, chunkSize, seq int,
	ch chan *protocol.Message) (*protocol.Message, error) {
	if err := b.Send(&protocol.Message{
		Type:      protocol.TypeFileGet,
		FileID:    fileID,
		Path:      path,
		ChunkSize: chunkSize,
		ChunkSeq:  seq,
	}); err != nil {
		return nil, err
	}
	msg := waitFileAck(ch)
	if msg == nil {
		return nil, errFileTimeout
	}
	if msg.Err != "" {
		return nil, errors.New(msg.Err)
	}
	return msg, nil
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