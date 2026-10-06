package core

// 文件传输的"待回包"注册表：
// 与 exec（一次性 Resolve 后删除）不同，文件传输一个 FileID 对应多片，
// 发起方（Web 控制台的 HTTP handler）用同一个 channel 逐片等待 ack/chunk，
// 全部完成或超时后由发起方 UnregisterFile；bot 下线时统一投递 nil 唤醒。

import "remoteassist-helper/protocol"

type fileWaiter struct {
	ch    chan *protocol.Message
	botID string
}

// RegisterFile 登记一个文件传输会话的等待通道；FileID 冲突返回 false
func (m *Manager) RegisterFile(fileID, botID string) (chan *protocol.Message, bool) {
	m.fileMu.Lock()
	defer m.fileMu.Unlock()
	if _, exists := m.fileWait[fileID]; exists {
		return nil, false
	}
	ch := make(chan *protocol.Message, 1)
	m.fileWait[fileID] = fileWaiter{ch: ch, botID: botID}
	return ch, true
}

// UnregisterFile 取消登记（传输完成 / 超时 / 连接关闭时）
func (m *Manager) UnregisterFile(fileID string) {
	m.fileMu.Lock()
	delete(m.fileWait, fileID)
	m.fileMu.Unlock()
}

// ResolveFile 投递一片 ack/chunk；无等待者时丢弃（可能已超时被 UnregisterFile）。
// 非阻塞投递：发起方放弃后 channel 可能残留，不能让 uplink 读协程卡住。
func (m *Manager) ResolveFile(msg *protocol.Message) bool {
	m.fileMu.Lock()
	w, ok := m.fileWait[msg.FileID]
	m.fileMu.Unlock()
	if !ok {
		return false
	}
	select {
	case w.ch <- msg:
	default:
		// 发起方已放弃（超时/关闭），丢弃这一片
	}
	return true
}

// failFilesOfBot bot 下线时，唤醒该 bot 所有未完成的文件传输（投递 nil 并删除）
func (m *Manager) failFilesOfBot(botID string) {
	m.fileMu.Lock()
	var pending []chan *protocol.Message
	for id, w := range m.fileWait {
		if w.botID == botID {
			delete(m.fileWait, id)
			pending = append(pending, w.ch)
		}
	}
	m.fileMu.Unlock()
	for _, ch := range pending {
		select {
		case ch <- nil:
		default:
		}
	}
}