// 用户端（被控机）入口：所有逻辑在 internal/agent，这里只负责启动。
package main

import "remoteassist-user/internal/agent"

func main() { agent.Run() }
