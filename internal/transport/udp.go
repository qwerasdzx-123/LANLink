// Package transport 传输层：ReliableUDP 信令通道 + TCP 数据通道。
//
// ReliableUDP 子层：对带 FlagNeedAck 的帧执行 ACK + 指数退避重传（最多 5 次），
// 接收侧按 (IP, MID) LRU 去重，保证"至少一次 + 应用层恰好一次"。
package transport

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"lanlink/internal/protocol"
)

// MulticastGroup 组播组（广播的冗余通道，应对部分 AP 只放行组播的环境）
var MulticastGroup = net.IPv4(239, 77, 77, 77)

// ErrSendTimeout 可靠发送重传耗尽
var ErrSendTimeout = errors.New("transport: 可靠发送超时（重传 5 次未收到 ACK）")

// Handler 帧处理回调
type Handler func(f *protocol.Frame, from *net.UDPAddr)

// UDP 可靠 UDP 信令通道
type UDP struct {
	conn  *net.UDPConn
	mconn *net.UDPConn // 组播接收（尽力而为）
	Port  int

	hmu      sync.RWMutex
	handlers map[uint16]Handler

	wmu     sync.Mutex
	waiters map[uint32]chan struct{}

	smu  sync.Mutex
	seen map[string]time.Time // (ip|mid) → 首见时间，重传去重
}

// NewUDP 监听指定端口
func NewUDP(port int) (*UDP, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: port})
	if err != nil {
		return nil, fmt.Errorf("transport: UDP 端口 %d 监听失败: %w", port, err)
	}
	u := &UDP{
		conn:     conn,
		Port:     conn.LocalAddr().(*net.UDPAddr).Port,
		handlers: make(map[uint16]Handler),
		waiters:  make(map[uint32]chan struct{}),
		seen:     make(map[string]time.Time),
	}
	// 组播接收（失败不致命）
	if mc, err := net.ListenMulticastUDP("udp4", nil, &net.UDPAddr{IP: MulticastGroup, Port: u.Port}); err == nil {
		u.mconn = mc
	} else {
		log.Printf("[transport] 组播监听失败（仅广播模式）: %v", err)
	}
	return u, nil
}

// RegisterHandler 按消息类型注册处理器（启动前完成注册）
func (u *UDP) RegisterHandler(typ uint16, h Handler) {
	u.hmu.Lock()
	u.handlers[typ] = h
	u.hmu.Unlock()
}

// Start 启动收包与去重表清理循环
func (u *UDP) Start(ctx context.Context) {
	go u.readLoop(ctx, u.conn)
	if u.mconn != nil {
		go u.readLoop(ctx, u.mconn)
	}
	go u.seenGC(ctx)
	go func() {
		<-ctx.Done()
		u.conn.Close()
		if u.mconn != nil {
			u.mconn.Close()
		}
	}()
}

func (u *UDP) readLoop(ctx context.Context, conn *net.UDPConn) {
	buf := make([]byte, 65536)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[transport] UDP 读取错误: %v", err)
			continue
		}
		f, err := protocol.Decode(buf[:n])
		if err != nil {
			continue // 非本协议流量，静默丢弃
		}
		u.dispatch(f, from)
	}
}

func (u *UDP) dispatch(f *protocol.Frame, from *net.UDPAddr) {
	// 1. ACK 帧：唤醒等待者
	if f.Type == protocol.TAck {
		u.wmu.Lock()
		if ch, ok := u.waiters[f.MID]; ok {
			close(ch)
			delete(u.waiters, f.MID)
		}
		u.wmu.Unlock()
		return
	}
	// 2. 需要 ACK 的帧：先回 ACK，再按 (IP, MID) 去重
	if f.Flags&protocol.FlagNeedAck != 0 {
		ack := &protocol.Frame{Type: protocol.TAck, MID: f.MID}
		_ = u.Send(from, ack)
		key := fmt.Sprintf("%s|%d", from.IP, f.MID)
		u.smu.Lock()
		if _, dup := u.seen[key]; dup {
			u.smu.Unlock()
			return // 重传副本，已处理过
		}
		u.seen[key] = time.Now()
		u.smu.Unlock()
	}
	// 3. 分发给业务处理器
	u.hmu.RLock()
	h := u.handlers[f.Type]
	u.hmu.RUnlock()
	if h == nil {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[transport] 处理器 panic (type=0x%04X): %v", f.Type, r)
			}
		}()
		h(f, from)
	}()
}

func (u *UDP) seenGC(ctx context.Context) {
	tk := time.NewTicker(30 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			cutoff := time.Now().Add(-2 * time.Minute)
			u.smu.Lock()
			for k, t := range u.seen {
				if t.Before(cutoff) {
					delete(u.seen, k)
				}
			}
			u.smu.Unlock()
		}
	}
}

// Send 单播发送（尽力而为）
func (u *UDP) Send(addr *net.UDPAddr, f *protocol.Frame) error {
	if len(f.Payload) > protocol.MaxUDPPayload {
		return protocol.ErrTooLarge
	}
	_, err := u.conn.WriteToUDP(f.Encode(), addr)
	return err
}

// SendReliable 可靠单播：ACK + 指数退避重传（300ms 起，×2，共 5 次）
func (u *UDP) SendReliable(ctx context.Context, addr *net.UDPAddr, f *protocol.Frame) error {
	if len(f.Payload) > protocol.MaxUDPPayload {
		return protocol.ErrTooLarge
	}
	f.Flags |= protocol.FlagNeedAck
	f.MID = newMID()

	ch := make(chan struct{})
	u.wmu.Lock()
	u.waiters[f.MID] = ch
	u.wmu.Unlock()
	defer func() {
		u.wmu.Lock()
		delete(u.waiters, f.MID)
		u.wmu.Unlock()
	}()

	raw := f.Encode()
	backoff := 300 * time.Millisecond
	for i := 0; i < 5; i++ {
		if _, err := u.conn.WriteToUDP(raw, addr); err != nil {
			return fmt.Errorf("transport: UDP 发送失败: %w", err)
		}
		select {
		case <-ch:
			return nil
		case <-time.After(backoff):
			backoff *= 2
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return ErrSendTimeout
}

// Broadcast 全网广播 + 组播冗余 + 各接口定向广播
func (u *UDP) Broadcast(f *protocol.Frame) error {
	if len(f.Payload) > protocol.MaxUDPPayload {
		return protocol.ErrTooLarge
	}
	raw := f.Encode()
	var firstErr error
	for _, addr := range broadcastTargets(u.Port) {
		if _, err := u.conn.WriteToUDP(raw, addr); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// broadcastTargets 汇总广播目标：全网广播、组播组、各网卡定向广播
func broadcastTargets(port int) []*net.UDPAddr {
	targets := []*net.UDPAddr{
		{IP: net.IPv4bcast, Port: port},
		{IP: MulticastGroup, Port: port},
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return targets
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			ip := ipnet.IP.To4()
			mask := ipnet.Mask
			bcast := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				bcast[i] = ip[i] | ^mask[i]
			}
			targets = append(targets, &net.UDPAddr{IP: bcast, Port: port})
		}
	}
	return targets
}

func newMID() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return uint32(time.Now().UnixNano())
	}
	return binary.BigEndian.Uint32(b[:])
}
