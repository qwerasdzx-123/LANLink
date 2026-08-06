package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// historyFileName 历史消息文件名（位于数据目录）
const historyFileName = "history.json"

// storedMsg 历史消息的磁盘存储结构（msgItem 为私有字段，需单独结构做 JSON 序列化）
type storedMsg struct {
	Sender string `json:"sender"`
	Text   string `json:"text"`
	Ts     int64  `json:"ts"`
	Mine   bool   `json:"mine"`
}

// loadHistory 从磁盘读取历史消息并合并进内存会话
func (g *gui) loadHistory() {
	g.mu.Lock()
	defer g.mu.Unlock()
	b, err := os.ReadFile(g.historyFile)
	if err != nil {
		return
	}
	var data map[string][]storedMsg
	if err := json.Unmarshal(b, &data); err != nil {
		return
	}
	for k, msgs := range data {
		if len(msgs) == 0 {
			continue
		}
		out := make([]msgItem, len(msgs))
		for i, m := range msgs {
			out[i] = msgItem{sender: m.Sender, text: m.Text, ts: m.Ts, mine: m.Mine}
		}
		g.convs[k] = out
	}
}

// saveHistory 将内存会话全量写入磁盘（先写临时文件再改名，避免写一半损坏）
func (g *gui) saveHistory() {
	g.mu.Lock()
	snap := make(map[string][]storedMsg, len(g.convs))
	for k, v := range g.convs {
		ss := make([]storedMsg, len(v))
		for i, m := range v {
			ss[i] = storedMsg{Sender: m.sender, Text: m.text, Ts: m.ts, Mine: m.mine}
		}
		snap[k] = ss
	}
	g.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(g.historyFile), 0o700); err != nil {
		return
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return
	}
	tmp := g.historyFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, g.historyFile)
}
