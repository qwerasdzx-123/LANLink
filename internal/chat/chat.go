// Package chat 聊天服务：单聊（端到端加密）、群聊（逐成员单播）、广播（明文+签名）、
// 离线队列（对端上线自动补发）、本地历史（JSONL 追加）。
package chat

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"lanlink/internal/bus"
	"lanlink/internal/discovery"
	"lanlink/internal/identity"
	"lanlink/internal/protocol"
	"lanlink/internal/secure"
	"lanlink/internal/transport"
)

// Message 聊天事件/历史记录
type Message struct {
	TS       int64  `json:"ts"`
	From     string `json:"from"`
	FromName string `json:"from_name"`
	To       string `json:"to,omitempty"`
	Group    string `json:"group,omitempty"`
	Text     string `json:"text"`
	Bcast    bool   `json:"bcast,omitempty"`
	Outgoing bool   `json:"out,omitempty"`
}

var ErrPeerOffline = errors.New("chat: 对端离线，消息已进入离线队列")

// Service 聊天服务
type Service struct {
	id   *identity.Identity
	udp  *transport.UDP
	disc *discovery.Discovery
	sec  *secure.Manager
	b    *bus.Bus

	dataDir string

	gmu    sync.Mutex
	groups map[string][]string // 群名 → 成员 peerID

	fmu sync.Mutex // 文件写互斥（历史/离线队列）
}

func New(id *identity.Identity, udp *transport.UDP, disc *discovery.Discovery,
	sec *secure.Manager, b *bus.Bus, dataDir string) (*Service, error) {

	s := &Service{
		id: id, udp: udp, disc: disc, sec: sec, b: b,
		dataDir: dataDir,
		groups:  make(map[string][]string),
	}
	for _, dir := range []string{s.historyDir(), s.outboxDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("chat: 创建目录失败: %w", err)
		}
	}
	if err := s.loadGroups(); err != nil {
		log.Printf("[chat] 加载群组失败（忽略）: %v", err)
	}
	udp.RegisterHandler(protocol.TMsgText, s.handleText)
	return s, nil
}

// Start 订阅上线事件，触发离线队列补发
func (s *Service) Start(ctx context.Context) {
	ch, cancel := s.b.Subscribe(128)
	go func() {
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if ev.Topic == bus.TopicPeerOnline {
					if pe, ok := ev.Data.(discovery.PeerEvent); ok {
						go s.flushOutbox(ctx, pe.ID)
					}
				}
				if ev.Topic == bus.TopicPeerOffline {
					if pe, ok := ev.Data.(discovery.PeerEvent); ok {
						s.sec.Drop(pe.ID) // 会话失效，重上线后重新握手
					}
				}
			}
		}
	}()
}

// ── 发送 ────────────────────────────────────────────────────

// SendText 单聊：对端在线走加密可靠单播；离线则入离线队列
func (s *Service) SendText(ctx context.Context, peerID, text string) error {
	return s.sendTo(ctx, peerID, "", text, true)
}

func (s *Service) sendTo(ctx context.Context, peerID, group, text string, allowQueue bool) error {
	peer, ok := s.disc.Get(peerID)
	if !ok || !peer.Online {
		if allowQueue {
			if err := s.queueOffline(peerID, group, text); err != nil {
				return err
			}
			s.b.Publish(bus.TopicMsgQueued, Message{To: peerID, Group: group, Text: text, TS: time.Now().UnixMilli()})
			return ErrPeerOffline
		}
		return fmt.Errorf("chat: 对端 %s 离线", peerID)
	}

	if err := s.sec.EnsureSession(ctx, peerID); err != nil {
		return fmt.Errorf("chat: 与 %s 握手失败: %w", peer.Name, err)
	}
	cipherText, err := s.sec.Seal(peerID, []byte(text))
	if err != nil {
		return fmt.Errorf("chat: 加密失败: %w", err)
	}
	msg := protocol.TextMsg{
		From: s.id.ID, FromName: s.id.Name(), Group: group,
		TS: time.Now().UnixMilli(), Enc: true, Cipher: cipherText,
	}
	f, err := protocol.NewFrame(protocol.TMsgText, &msg)
	if err != nil {
		return err
	}
	f.Flags |= protocol.FlagEnc

	addr, _ := s.disc.PeerUDPAddr(peerID)
	if err := s.udp.SendReliable(ctx, addr, f); err != nil {
		if allowQueue && errors.Is(err, transport.ErrSendTimeout) {
			_ = s.queueOffline(peerID, group, text)
			s.b.Publish(bus.TopicMsgQueued, Message{To: peerID, Group: group, Text: text, TS: msg.TS})
			return ErrPeerOffline
		}
		s.b.Publish(bus.TopicMsgFailed, Message{To: peerID, Group: group, Text: text, TS: msg.TS})
		return fmt.Errorf("chat: 发送失败: %w", err)
	}

	rec := Message{TS: msg.TS, From: s.id.ID, FromName: s.id.Name(), To: peerID, Group: group, Text: text, Outgoing: true}
	s.appendHistory(peerID, rec)
	s.b.Publish(bus.TopicMsgSent, rec)
	return nil
}

// Broadcast 广播消息：明文 + Ed25519 签名（防伪造）
func (s *Service) Broadcast(text string) error {
	ts := time.Now().UnixMilli()
	msg := protocol.TextMsg{
		From: s.id.ID, FromName: s.id.Name(),
		TS: ts, Enc: false, Body: text, Bcast: true,
		Sig: s.id.Sign(bcastDigest(s.id.ID, text, ts)),
	}
	f, err := protocol.NewFrame(protocol.TMsgText, &msg)
	if err != nil {
		return err
	}
	if err := s.udp.Broadcast(f); err != nil {
		return fmt.Errorf("chat: 广播失败: %w", err)
	}
	s.appendHistory("_broadcast", Message{TS: ts, From: s.id.ID, FromName: s.id.Name(), Text: text, Bcast: true, Outgoing: true})
	return nil
}

// ── 群聊 ────────────────────────────────────────────────────

// CreateGroup 创建/覆盖群组（本地成员表，逐成员加密单播）
func (s *Service) CreateGroup(name string, members []string) error {
	if name == "" || len(members) == 0 {
		return errors.New("chat: 群名与成员不能为空")
	}
	s.gmu.Lock()
	s.groups[name] = append([]string(nil), members...)
	s.gmu.Unlock()
	return s.saveGroups()
}

// Groups 群组快照
func (s *Service) Groups() map[string][]string {
	s.gmu.Lock()
	defer s.gmu.Unlock()
	out := make(map[string][]string, len(s.groups))
	for k, v := range s.groups {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// SendGroup 群发：逐成员可靠单播（可靠性优于组播）
func (s *Service) SendGroup(ctx context.Context, group, text string) error {
	s.gmu.Lock()
	members, ok := s.groups[group]
	s.gmu.Unlock()
	if !ok {
		return fmt.Errorf("chat: 群组 %q 不存在", group)
	}
	var errs []string
	for _, m := range members {
		if m == s.id.ID {
			continue
		}
		if err := s.sendTo(ctx, m, group, text, true); err != nil && !errors.Is(err, ErrPeerOffline) {
			errs = append(errs, fmt.Sprintf("%s: %v", m, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("chat: 群发部分失败: %s", strings.Join(errs, "; "))
	}
	return nil
}

// ── 接收 ────────────────────────────────────────────────────

func (s *Service) handleText(f *protocol.Frame, from *net.UDPAddr) {
	var msg protocol.TextMsg
	if err := protocol.DecodePayload(f, &msg); err != nil {
		return
	}
	if msg.From == s.id.ID {
		return // 自己的广播回环
	}

	var text string
	if msg.Enc {
		plain, err := s.sec.Open(msg.From, msg.Cipher)
		if err != nil {
			log.Printf("[chat] 解密来自 %s 的消息失败: %v", msg.From, err)
			return
		}
		text = string(plain)
	} else {
		// 广播明文：能验签则验签，公钥未知（尚未发现对方）时提示不可信
		if pub, ok := s.disc.PeerPubKey(msg.From); ok {
			if !identity.Verify(pub, bcastDigest(msg.From, msg.Body, msg.TS), msg.Sig) {
				log.Printf("[chat] 丢弃签名非法的广播消息 (来自 %s)", from)
				return
			}
		} else {
			log.Printf("[chat] 收到未知节点 %s 的广播（未验签）", msg.From)
		}
		text = msg.Body
	}

	rec := Message{
		TS: msg.TS, From: msg.From, FromName: msg.FromName,
		Group: msg.Group, Text: text, Bcast: msg.Bcast,
	}
	histKey := msg.From
	if msg.Bcast {
		histKey = "_broadcast"
	} else if msg.Group != "" {
		histKey = "group_" + msg.Group
	}
	s.appendHistory(histKey, rec)
	s.b.Publish(bus.TopicMsgRecv, rec)
}

func bcastDigest(from, body string, ts int64) []byte {
	h := sha256.New()
	h.Write([]byte("llp-bc"))
	h.Write([]byte(from))
	h.Write([]byte(body))
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(ts))
	h.Write(b[:])
	return h.Sum(nil)
}

// ── 离线队列 ────────────────────────────────────────────────

type outboxItem struct {
	Group string `json:"group,omitempty"`
	Text  string `json:"text"`
	TS    int64  `json:"ts"`
}

func (s *Service) queueOffline(peerID, group, text string) error {
	s.fmu.Lock()
	defer s.fmu.Unlock()
	path := filepath.Join(s.outboxDir(), peerID+".jsonl")
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("chat: 打开离线队列失败: %w", err)
	}
	defer fh.Close()
	b, _ := json.Marshal(outboxItem{Group: group, Text: text, TS: time.Now().UnixMilli()})
	_, err = fh.Write(append(b, '\n'))
	return err
}

// flushOutbox 对端上线后补发离线消息
func (s *Service) flushOutbox(ctx context.Context, peerID string) {
	path := filepath.Join(s.outboxDir(), peerID+".jsonl")

	s.fmu.Lock()
	data, err := os.ReadFile(path)
	if err != nil { // 不存在即无积压
		s.fmu.Unlock()
		return
	}
	_ = os.Remove(path) // 先摘走，失败的重新入队
	s.fmu.Unlock()

	sc := bufio.NewScanner(strings.NewReader(string(data)))
	count := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var it outboxItem
		if err := json.Unmarshal([]byte(line), &it); err != nil {
			continue
		}
		if err := s.sendTo(ctx, peerID, it.Group, it.Text, false); err != nil {
			_ = s.queueOffline(peerID, it.Group, it.Text) // 仍失败，重新排队
			continue
		}
		count++
	}
	if count > 0 {
		log.Printf("[chat] 已向 %s 补发 %d 条离线消息", peerID, count)
	}
}

// ── 历史持久化 ──────────────────────────────────────────────

func (s *Service) historyDir() string { return filepath.Join(s.dataDir, "history") }
func (s *Service) outboxDir() string  { return filepath.Join(s.dataDir, "outbox") }

func (s *Service) appendHistory(key string, m Message) {
	s.fmu.Lock()
	defer s.fmu.Unlock()
	path := filepath.Join(s.historyDir(), sanitize(key)+".jsonl")
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("[chat] 写历史失败: %v", err)
		return
	}
	defer fh.Close()
	b, _ := json.Marshal(m)
	if _, err := fh.Write(append(b, '\n')); err != nil {
		log.Printf("[chat] 写历史失败: %v", err)
	}
}

// History 读取与某会话的最近 n 条历史
func (s *Service) History(key string, n int) ([]Message, error) {
	path := filepath.Join(s.historyDir(), sanitize(key)+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := make([]Message, 0, len(lines))
	for _, l := range lines {
		var m Message
		if json.Unmarshal([]byte(l), &m) == nil {
			out = append(out, m)
		}
	}
	return out, nil
}

// ── 群组持久化 ──────────────────────────────────────────────

func (s *Service) groupsPath() string { return filepath.Join(s.dataDir, "groups.json") }

func (s *Service) saveGroups() error {
	s.gmu.Lock()
	b, err := json.MarshalIndent(s.groups, "", "  ")
	s.gmu.Unlock()
	if err != nil {
		return err
	}
	return os.WriteFile(s.groupsPath(), b, 0o644)
}

func (s *Service) loadGroups() error {
	data, err := os.ReadFile(s.groupsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	s.gmu.Lock()
	defer s.gmu.Unlock()
	return json.Unmarshal(data, &s.groups)
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		}
		return r
	}, s)
}
