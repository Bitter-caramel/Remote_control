package agent

import (
	"time"

	"remoteassist-user/protocol"
)

// StartHeartbeat 周期向协助者发送心跳。
// 心跳只用于检测连接是否存在，不写入任何日志。
func StartHeartbeat(c *Client, stop <-chan struct{}) {
	go func() {
		ticker := time.NewTicker(protocol.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := c.Send(&protocol.Message{Type: protocol.TypeHeartbeat}); err != nil {
					return // 发送失败说明连接已断，读循环会感知并重连
				}
			case <-stop:
				return
			}
		}
	}()
}
