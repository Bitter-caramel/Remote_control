package core

// events：占用 / 排队 / 释放的播报历史。
//
// 这些事件只在「有人接入或离开终端、排队发生变动」时产生，频次极低，
// 因此用内存定长环形缓冲即可，不落库（避免为它做表结构迁移与每次连接写盘）。
// 定长环保证内存上界、O(1) 追加，也免去 GC 抖动。

import (
	"sync"
	"time"
)

// eventRingSize 播报历史保留条数上限
const eventRingSize = 500

// EventKind 播报事件类型
type EventKind string

const (
	EventOccupy  EventKind = "occupy"  // 开始占用
	EventRelease EventKind = "release" // 释放控制权
	EventQueue   EventKind = "queue"   // 加入排队
)

// Event 一条播报记录。BotName 与 Actor 都是事件发生时的快照，
// 这样机器下线或账号改名之后，历史记录仍能正常显示。
type Event struct {
	Seq      uint64    `json:"seq"`
	Time     time.Time `json:"time"`
	Kind     EventKind `json:"kind"`
	BotID    string    `json:"bot_id"`
	BotName  string    `json:"bot_name"`
	Actor    string    `json:"actor"`    // 展示名
	Username string    `json:"username"` // 本机终端为空
	Detail   string    `json:"detail"`
}

// EventLog 定长环形缓冲，并发安全
type EventLog struct {
	mu   sync.Mutex
	ring []Event
	next int    // 下一个写入位置
	seq  uint64 // 自增序号，用来区分还没填满的空槽
}

// NewEventLog 建一个容量为 size 的播报历史缓冲
func NewEventLog(size int) *EventLog {
	if size <= 0 {
		size = eventRingSize
	}
	return &EventLog{ring: make([]Event, size)}
}

// Append 追加一条记录，Seq 与 Time 由这里补齐
func (e *EventLog) Append(ev Event) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	ev.Seq = e.seq
	if ev.Time.IsZero() {
		ev.Time = time.Now()
	}
	e.ring[e.next] = ev
	e.next = (e.next + 1) % len(e.ring)
}

// Snapshot 按时间由新到旧返回全部记录的副本
func (e *EventLog) Snapshot() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := len(e.ring)
	out := make([]Event, 0, n)
	for i := 1; i <= n; i++ {
		idx := (e.next - i + n) % n
		if e.ring[idx].Seq == 0 {
			continue // 还没填满的空槽
		}
		out = append(out, e.ring[idx])
	}
	return out
}
