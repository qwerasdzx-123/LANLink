// Package discovery 节点发现：
//
//	上线突发  启动后 0s/1s/3s 连发 3 次 ONLINE（≤2s 感知，抗首包丢失）
//	稳态心跳  每 10s 广播 HEARTBEAT
//	离线判定  30s 未见 → 单播探测 2 次 → 判定离线
//	安全      通告带 Ed25519 签名；公钥 TOFU 钉扎，变更即告警
package discovery

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"log"
	"net"
	"sort"
	"sync"
	"time"

	"lanlink/internal/bus"
	"lanlink/internal/identity"
	"lanlink/internal/protocol"
	"lanlink/internal/transport"
)

const (
	heartbeatEvery = 10 * time.Second
	reapEvery      = 5 * time.Second
	staleAfter     = 30 * time.Second
	maxProbes      = 2
)

// Peer 在线节点视图
type Peer struct {
	ID       string
	Name     string
	IP       net.IP
	UDPPort  int
	TCPPort  int
	PubKey   ed25519.PublicKey
	LastSeen time.Time
	Online   bool

	probes int
}

// PeerEvent 上下线事件载荷
type PeerEvent struct {
	ID   string
	Name string
	Addr string
}

// Discovery 节点发现服务
type Discovery struct {
	id      *identity.Identity
	udp     *transport.UDP
	b       *bus.Bus
	tcpPort int

	mu    sync.RWMutex
	peers map[string]*Peer
}

func New(id *identity.Identity, udp *transport.UDP, b *bus.Bus, tcpPort int) *Discovery {
	d := &Discovery{
		id:      id,
		udp:     udp,
		b:       b,
		tcpPort: tcpPort,
		peers:   make(map[string]*Peer),
	}
	udp.RegisterHandler(protocol.TOnline, d.handleAnnounce(true))
	udp.RegisterHandler(protocol.TOnlineAck, d.handleAnnounce(false))
	udp.RegisterHandler(protocol.THeartbeat, d.handleAnnounce(false))
	udp.RegisterHandler(protocol.TOffline, d.handleOffline)
	return d
}

// Start 启动通告与保活循环
func (d *Discovery) Start(ctx context.Context) {
	go func() {
		// 上线突发期：0s / 1s / 3s
		for _, delay := range []time.Duration{0, time.Second, 2 * time.Second} {
			select {
			case <-time.After(delay):
				d.broadcastAnnounce(protocol.TOnline)
			case <-ctx.Done():
				return
			}
		}
		// 稳态心跳
		tk := time.NewTicker(heartbeatEvery)
		defer tk.Stop()
		for {
			select {
			case <-tk.C:
				d.broadcastAnnounce(protocol.THeartbeat)
			case <-ctx.Done():
				return
			}
		}
	}()
	go d.reapLoop(ctx)
}

// Stop 尽力广播下线
func (d *Discovery) Stop() {
	f, err := protocol.NewFrame(protocol.TOffline, d.selfInfo())
	if err == nil {
		_ = d.udp.Broadcast(f)
	}
}

func (d *Discovery) selfInfo() *protocol.PeerInfo {
	info := &protocol.PeerInfo{
		ID:      d.id.ID,
		Name:    d.id.Name(),
		UDPPort: d.udp.Port,
		TCPPort: d.tcpPort,
		PubKey:  d.id.Pub,
		TS:      time.Now().UnixMilli(),
	}
	info.Sig = d.id.Sign(announceDigest(info))
	return info
}

func (d *Discovery) broadcastAnnounce(typ uint16) {
	f, err := protocol.NewFrame(typ, d.selfInfo())
	if err != nil {
		log.Printf("[discovery] 构造通告失败: %v", err)
		return
	}
	if err := d.udp.Broadcast(f); err != nil {
		log.Printf("[discovery] 广播失败: %v", err)
	}
}

// handleAnnounce 处理 ONLINE / ONLINE_ACK / HEARTBEAT
func (d *Discovery) handleAnnounce(replyAck bool) transport.Handler {
	return func(f *protocol.Frame, from *net.UDPAddr) {
		var info protocol.PeerInfo
		if err := protocol.DecodePayload(f, &info); err != nil {
			return
		}
		if info.ID == d.id.ID {
			return // 自己的广播回环
		}
		if !identity.Verify(info.PubKey, announceDigest(&info), info.Sig) {
			log.Printf("[discovery] 丢弃签名非法的通告 (来自 %s)", from)
			return
		}

		d.mu.Lock()
		p, exists := d.peers[info.ID]
		if exists {
			// TOFU：公钥钉扎，变更即安全告警并拒绝更新
			if !p.PubKey.Equal(ed25519.PublicKey(info.PubKey)) {
				d.mu.Unlock()
				d.b.Publish(bus.TopicPeerWarn, PeerEvent{
					ID: info.ID, Name: info.Name,
					Addr: "公钥指纹变更！可能存在身份仿冒: " + identity.Fingerprint(info.PubKey),
				})
				return
			}
			wasOffline := !p.Online
			p.Name, p.IP, p.UDPPort, p.TCPPort = info.Name, from.IP, from.Port, info.TCPPort
			p.Name = info.Name // 对方可能已改名，心跳时同步
			p.LastSeen, p.Online, p.probes = time.Now(), true, 0
			d.mu.Unlock()
			if wasOffline {
				d.b.Publish(bus.TopicPeerOnline, PeerEvent{ID: info.ID, Name: info.Name, Addr: from.String()})
			}
		} else {
			d.peers[info.ID] = &Peer{
				ID: info.ID, Name: info.Name,
				IP: from.IP, UDPPort: from.Port, TCPPort: info.TCPPort,
				PubKey: append(ed25519.PublicKey(nil), info.PubKey...),
				LastSeen: time.Now(), Online: true,
			}
			d.mu.Unlock()
			d.b.Publish(bus.TopicPeerOnline, PeerEvent{ID: info.ID, Name: info.Name, Addr: from.String()})
		}

		if replyAck { // 对 ONLINE 单播回 ACK，令新节点一轮建全量节点表
			ack, err := protocol.NewFrame(protocol.TOnlineAck, d.selfInfo())
			if err == nil {
				_ = d.udp.Send(from, ack)
			}
		}
	}
}

func (d *Discovery) handleOffline(f *protocol.Frame, from *net.UDPAddr) {
	var info protocol.PeerInfo
	if err := protocol.DecodePayload(f, &info); err != nil {
		return
	}
	d.mu.Lock()
	p, ok := d.peers[info.ID]
	if !ok || !p.Online || !identity.Verify(p.PubKey, announceDigest(&info), info.Sig) {
		d.mu.Unlock()
		return
	}
	p.Online = false
	d.mu.Unlock()
	d.b.Publish(bus.TopicPeerOffline, PeerEvent{ID: info.ID, Name: p.Name, Addr: from.String()})
}

// reapLoop 超时判定：30s 未见 → 单播探测 → 仍无响应判离线
func (d *Discovery) reapLoop(ctx context.Context) {
	tk := time.NewTicker(reapEvery)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			var probeAddrs []*net.UDPAddr
			var offline []PeerEvent
			d.mu.Lock()
			for _, p := range d.peers {
				if !p.Online || time.Since(p.LastSeen) < staleAfter {
					continue
				}
				if p.probes < maxProbes {
					p.probes++
					probeAddrs = append(probeAddrs, &net.UDPAddr{IP: p.IP, Port: p.UDPPort})
				} else {
					p.Online = false
					offline = append(offline, PeerEvent{ID: p.ID, Name: p.Name})
				}
			}
			d.mu.Unlock()

			for _, addr := range probeAddrs { // 单播 ONLINE 探测（对方会回 ONLINE_ACK）
				f, err := protocol.NewFrame(protocol.TOnline, d.selfInfo())
				if err == nil {
					_ = d.udp.Send(addr, f)
				}
			}
			for _, ev := range offline {
				d.b.Publish(bus.TopicPeerOffline, ev)
			}
		}
	}
}

// RemovePeer 从节点表中删除指定设备（用于用户手动移除旧设备记录）
func (d *Discovery) RemovePeer(id string) {
	d.mu.Lock()
	delete(d.peers, id)
	d.mu.Unlock()
}

// Refresh 立即触发一次上线通告广播，用于手动刷新设备列表。
// 注意：必须用 TOnline（而非 THeartbeat），因为收到 TOnline 的对端会回
// ONLINE_ACK 自我通报，这样本节点才能重新学习到对方；HEARTBEAT 不会触发回包。
func (d *Discovery) Refresh() {
	d.broadcastAnnounce(protocol.TOnline)
}

// ── 查询接口 ────────────────────────────────────────────────

// Peers 按名称排序的节点快照（含在线和离线设备）
func (d *Discovery) Peers() []Peer {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]Peer, 0, len(d.peers))
	for _, p := range d.peers {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name+out[i].ID < out[j].Name+out[j].ID })
	return out
}

// Get 按 ID 查询
func (d *Discovery) Get(peerID string) (Peer, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	p, ok := d.peers[peerID]
	if !ok {
		return Peer{}, false
	}
	return *p, true
}

// ── 实现 secure.PeerDirectory ───────────────────────────────

func (d *Discovery) PeerPubKey(peerID string) (ed25519.PublicKey, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	p, ok := d.peers[peerID]
	if !ok {
		return nil, false
	}
	return p.PubKey, true
}

func (d *Discovery) PeerUDPAddr(peerID string) (*net.UDPAddr, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	p, ok := d.peers[peerID]
	if !ok {
		return nil, false
	}
	return &net.UDPAddr{IP: p.IP, Port: p.UDPPort}, true
}

// announceDigest 通告签名摘要：H(id||name||ts||pubkey)
func announceDigest(p *protocol.PeerInfo) []byte {
	h := sha256.New()
	h.Write([]byte(p.ID))
	h.Write([]byte(p.Name))
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(p.TS))
	h.Write(ts[:])
	h.Write(p.PubKey)
	return h.Sum(nil)
}
