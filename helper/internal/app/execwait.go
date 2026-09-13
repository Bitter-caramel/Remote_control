package app

// exec 请求的"待回包"注册表：
// AttachServer 发出 exec 后按 MsgID 登记一个 channel，
// listener 收到该 bot 的 exec_result 时按 MsgID 投递回来；
// bot 下线时该 bot 所有未决请求立即收到 nil。

import "remoteassist-helper/protocol"

// BotInfo 对外（CLI/Agent）暴露的 bot 摘要信息
type BotInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"` // 自定义名称（空串表示未设置）
	OS       string `json:"os"`   // 系统信息
	Addr     string `json:"addr"`
	Buffered bool   `json:"buffered"` // 是否处于心跳缓存区（疑似掉线）
}

type execWaiter struct {
	ch    chan *protocol.Message
	botID string
}

// RegisterExec 登记一个等待结果的 exec 请求；MsgID 冲突返回 false
func (m *Manager) RegisterExec(msgID, botID string, ch chan *protocol.Message) bool {
	m.execMu.Lock()
	defer m.execMu.Unlock()
	if _, exists := m.execWait[msgID]; exists {
		return false
	}
	m.execWait[msgID] = execWaiter{ch: ch, botID: botID}
	return true
}

// UnregisterExec 取消登记（超时/连接关闭时）
func (m *Manager) UnregisterExec(msgID string) {
	m.execMu.Lock()
	delete(m.execWait, msgID)
	m.execMu.Unlock()
}

// ResolveExec 投递 exec 结果；无等待者时丢弃（可能已超时）
func (m *Manager) ResolveExec(msg *protocol.Message) bool {
	m.execMu.Lock()
	w, ok := m.execWait[msg.MsgID]
	if ok {
		delete(m.execWait, msg.MsgID)
	}
	m.execMu.Unlock()
	if !ok {
		return false
	}
	w.ch <- msg
	return true
}

// failExecsOfBot bot 下线时，唤醒该 bot 所有未决 exec（投递 nil）
func (m *Manager) failExecsOfBot(botID string) {
	m.execMu.Lock()
	var pending []chan *protocol.Message
	for id, w := range m.execWait {
		if w.botID == botID {
			delete(m.execWait, id)
			pending = append(pending, w.ch)
		}
	}
	m.execMu.Unlock()
	for _, ch := range pending {
		ch <- nil
	}
}

// BotInfos 返回在线 bot 的 JSON 友好摘要
func (m *Manager) BotInfos() []BotInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := make([]BotInfo, 0, len(m.bots))
	for _, b := range m.bots {
		list = append(list, BotInfo{
			ID:       b.ID,
			Name:     b.Name,
			OS:       b.OS,
			Addr:     b.Addr(),
			Buffered: b.Buffered,
		})
	}
	return list
}
