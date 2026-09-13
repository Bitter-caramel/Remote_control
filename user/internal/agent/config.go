package agent

import (
	"encoding/json"
	"os"
)

// Config 用户端本地配置文件（config.json），靠 userID 识别身份。
// userID 与服务端的 botID 是同一个东西：首次上线由协助者分配，之后固定不变。
type Config struct {
	ServerAddr string `json:"server_addr"` // 协助者服务器的 IP 和端口（提前写入）
	UserID     string `json:"user_id"`     // 首次上线后由服务端分配并保存
	Name       string `json:"name"`        // 本机自定义名称，可自行填写（如"财务室-电脑"）；留空则协助端只显示 botID
}

// defaultServerAddr 协助者服务器的地址，发布前提前写入。
// 首次生成 config 文件时写入，之后可在 config.json 中直接修改。
const defaultServerAddr = "127.0.0.1:8080"

const configPath = "config.json"

// LoadConfig 检测 config 文件：
//   - 不存在 → 创建 config 文件（此时 userID 为空，视为首次上线，向协助者申请 userID）
//   - 存在   → 读取，直接使用里面的 userID
func LoadConfig() (*Config, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		cfg := &Config{ServerAddr: defaultServerAddr}
		if err := cfg.Save(); err != nil {
			return nil, err
		}
		return cfg, nil
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.ServerAddr == "" {
		cfg.ServerAddr = defaultServerAddr
		if err := cfg.Save(); err != nil {
			return nil, err
		}
	}
	return &cfg, nil
}

// Save 把配置写回 config.json
func (c *Config) Save() error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, data, 0o644)
}

// FirstTime 是否首次上线（还没有 userID）
func (c *Config) FirstTime() bool { return c.UserID == "" }
