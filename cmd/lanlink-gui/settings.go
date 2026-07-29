package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const settingsFile = "gui_settings.json"

// settings GUI 持久化设置（保存于数据目录 gui_settings.json）
type settings struct {
	Nickname    string            `json:"nickname"`      // 显示昵称（覆盖 -name）
	Avatar      string            `json:"avatar"`        // 头像文件路径
	Dark        bool              `json:"dark"`          // 暗黑主题
	Subnet      string            `json:"subnet"`        // 局域网段过滤（CIDR，空=不过滤）
	AutoStart   bool              `json:"auto_start"`    // 开机自动启动
	HideIP      bool              `json:"hide_ip"`       // 界面隐藏 IP 地址
	NotifyPopup bool              `json:"notify_popup"`  // true=自动弹出窗口 false=任务栏闪烁
	Sound       bool              `json:"sound"`         // 新消息提示音
	CloseToTray bool              `json:"close_to_tray"` // 关闭按钮最小化到托盘
	Downloads   string            `json:"downloads"`     // 接收文件目录（覆盖 -dl）
	AutoOpen    bool              `json:"auto_open"`     // 接收完成后自动打开文件
	Remarks     map[string]string `json:"remarks"`       // peerID → 备注名
}

// loadSettings 读取设置，不存在时返回默认值
func loadSettings(dataDir string) *settings {
	s := &settings{
		Sound:       true,
		CloseToTray: true,
		Remarks:     map[string]string{},
	}
	b, err := os.ReadFile(filepath.Join(dataDir, settingsFile))
	if err == nil {
		_ = json.Unmarshal(b, s)
	}
	if s.Remarks == nil {
		s.Remarks = map[string]string{}
	}
	return s
}

// save 持久化设置
func (s *settings) save(dataDir string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, settingsFile), b, 0o600)
}
