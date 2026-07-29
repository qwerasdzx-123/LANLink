// Package bus 进程内事件总线：服务层 → UI/CLI 的严格单向数据流。
// Publish 永不阻塞（订阅者队列满则丢弃并计数），保证网络协程不被 UI 拖死。
package bus

import (
	"sync"
	"sync/atomic"
)

// 事件主题约定
const (
	TopicPeerOnline       = "peer:online"
	TopicPeerOffline      = "peer:offline"
	TopicPeerWarn         = "peer:warn" // 公钥变更等安全告警
	TopicMsgRecv          = "msg:recv"
	TopicMsgSent          = "msg:sent"
	TopicMsgFailed        = "msg:failed"
	TopicMsgQueued        = "msg:queued" // 对端离线，进入离线队列
	TopicTransferOffer    = "transfer:offer"
	TopicTransferProgress = "transfer:progress"
	TopicTransferDone     = "transfer:done"
	TopicTransferError    = "transfer:error"
	TopicTransferPaused   = "transfer:paused"
)

// Event 总线事件
type Event struct {
	Topic string
	Data  any
}

type subscriber struct {
	ch      chan Event
	dropped atomic.Int64
}

// Bus 多订阅者事件总线
type Bus struct {
	mu   sync.RWMutex
	subs map[int]*subscriber
	next int
}

func New() *Bus {
	return &Bus{subs: make(map[int]*subscriber)}
}

// Subscribe 订阅全部事件；返回只读通道与取消函数
func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer <= 0 {
		buffer = 256
	}
	s := &subscriber{ch: make(chan Event, buffer)}
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = s
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		if cur, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(cur.ch)
		}
		b.mu.Unlock()
	}
	return s.ch, cancel
}

// Publish 非阻塞发布；队列满则丢弃（背压保护）
func (b *Bus) Publish(topic string, data any) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, s := range b.subs {
		select {
		case s.ch <- Event{Topic: topic, Data: data}:
		default:
			s.dropped.Add(1)
		}
	}
}
