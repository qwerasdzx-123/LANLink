// Package transfer 文件传输：
//
//	信令  UDP 可靠单播 FILE_OFFER / FILE_ANSWER
//	数据  接收方主动连接发送方 TCP，HELLO 绑定 → 逐文件 PULL 拉取
//	续传  .part + .llmeta（size/sha256/sender 三要素校验）
//	限速  发送端令牌桶（rate.Limiter），运行中可调
//	校验  SHA-256 流式哈希，文件级端到端校验
package transfer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"lanlink/internal/bus"
	"lanlink/internal/discovery"
	"lanlink/internal/identity"
	"lanlink/internal/protocol"
	"lanlink/internal/secure"
	"lanlink/internal/transport"
)

const (
	chunkSize    = 256 * 1024      // 256KB 分块
	metaEvery    = 4 * 1024 * 1024 // 每 4MB 持久化一次续传位点
	progressGap  = 500 * time.Millisecond
	streamRWTime = 60 * time.Second
)

// 任务状态
type State string

const (
	StateOffering     State = "OFFERING"     // 已发出请求，等待应答
	StatePendingLocal State = "PENDING"      // 收到请求，等待本地用户接受
	StateTransferring State = "TRANSFERRING" // 传输中
	StatePaused       State = "PAUSED"       // 已暂停（可续传）
	StateCompleted    State = "COMPLETED"    // 完成
	StateFailed       State = "FAILED"       // 失败
	StateRejected     State = "REJECTED"     // 被拒绝
)

// Direction 任务方向
type Direction string

const (
	DirSend Direction = "send"
	DirRecv Direction = "recv"
)

// Task 传输任务
type Task struct {
	ID       string
	PeerID   string
	PeerName string
	Dir      Direction
	Files    []protocol.FileEntry
	Token    string
	Total    int64
	State    State

	// 发送端：清单下标 → 本地绝对路径
	absPaths map[int]string
	// 接收端：发送方 TCP 地址
	senderAddr string

	mu        sync.Mutex
	done      int64
	lastBytes int64
	lastTime  time.Time
	speed     float64 // B/s
	cancel    context.CancelFunc
	pausing   bool
	lastEvent time.Time
}

// TaskView 供 UI/CLI 展示的任务快照
type TaskView struct {
	ID       string
	PeerName string
	Dir      Direction
	State    State
	Files    int
	Done     int64
	Total    int64
	SpeedBps float64
}

// ProgressEvent 进度事件
type ProgressEvent struct {
	TaskID   string
	PeerName string
	File     string
	Done     int64
	Total    int64
	SpeedBps float64
}

// OfferEvent 收到文件请求事件
type OfferEvent struct {
	TaskID   string
	PeerName string
	Files    int
	Total    int64
}

// ResultEvent 完成/失败事件
type ResultEvent struct {
	TaskID   string
	PeerName string
	Message  string
}

// Service 文件传输服务
type Service struct {
	id   *identity.Identity
	udp  *transport.UDP
	tcp  *transport.TCPServer
	disc *discovery.Discovery
	sec  *secure.Manager
	b    *bus.Bus

	dmu       sync.RWMutex
	downloads string

	tmu   sync.RWMutex
	tasks map[string]*Task

	limiter *rate.Limiter // 发送端令牌桶
	pool    sync.Pool     // 256KB 块缓冲复用
}

func New(id *identity.Identity, udp *transport.UDP, tcp *transport.TCPServer,
	disc *discovery.Discovery, sec *secure.Manager, b *bus.Bus, downloads string) (*Service, error) {

	if err := os.MkdirAll(downloads, 0o755); err != nil {
		return nil, fmt.Errorf("transfer: 创建下载目录失败: %w", err)
	}
	s := &Service{
		id: id, udp: udp, tcp: tcp, disc: disc, sec: sec, b: b,
		downloads: downloads,
		tasks:     make(map[string]*Task),
		limiter:   rate.NewLimiter(rate.Inf, 1<<20),
		pool: sync.Pool{New: func() any {
			buf := make([]byte, chunkSize)
			return &buf
		}},
	}
	udp.RegisterHandler(protocol.TFileOffer, s.handleOffer)
	udp.RegisterHandler(protocol.TFileAns, s.handleAnswer)
	return s, nil
}

// Start 启动 TCP 数据通道服务
func (s *Service) Start(ctx context.Context) {
	go s.tcp.Serve(ctx, s.handleConn)
}

// SetDownloads 运行时修改下载目录（对后续新任务生效）
func (s *Service) SetDownloads(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("transfer: 创建下载目录失败: %w", err)
	}
	s.dmu.Lock()
	s.downloads = dir
	s.dmu.Unlock()
	return nil
}

// Downloads 当前下载目录
func (s *Service) Downloads() string {
	s.dmu.RLock()
	defer s.dmu.RUnlock()
	return s.downloads
}

// SetRateKBps 设置发送限速（0 = 不限速），运行中即时生效
func (s *Service) SetRateKBps(kbps int) {
	if kbps <= 0 {
		s.limiter.SetLimit(rate.Inf)
		return
	}
	s.limiter.SetLimit(rate.Limit(kbps * 1024))
	s.limiter.SetBurst(maxInt(kbps*1024/4, chunkSize))
}

// SavedPaths 返回某接收任务的本地保存路径（按 ID 前缀匹配），用于「打开文件」按钮。
// 若任务非接收任务或多文件，返回每个文件相对于下载根目录的完整路径。
func (s *Service) SavedPaths(prefix string) ([]string, error) {
	task, err := s.FindTask(prefix)
	if err != nil {
		return nil, err
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	if task.Dir != DirRecv {
		return nil, fmt.Errorf("非接收任务")
	}
	root := s.Downloads()
	out := make([]string, 0, len(task.Files))
	for _, e := range task.Files {
		out = append(out, filepath.Join(root, filepath.FromSlash(e.Rel)))
	}
	return out, nil
}

// Tasks 任务快照
func (s *Service) Tasks() []TaskView {
	s.tmu.RLock()
	defer s.tmu.RUnlock()
	out := make([]TaskView, 0, len(s.tasks))
	for _, t := range s.tasks {
		t.mu.Lock()
		out = append(out, TaskView{
			ID: t.ID, PeerName: t.PeerName, Dir: t.Dir, State: t.State,
			Files: len(t.Files), Done: t.done, Total: t.Total, SpeedBps: t.speed,
		})
		t.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// FindTask 按 ID 前缀查找任务
func (s *Service) FindTask(prefix string) (*Task, error) {
	s.tmu.RLock()
	defer s.tmu.RUnlock()
	var hit *Task
	for id, t := range s.tasks {
		if strings.HasPrefix(id, prefix) {
			if hit != nil {
				return nil, fmt.Errorf("transfer: 任务前缀 %q 有歧义", prefix)
			}
			hit = t
		}
	}
	if hit == nil {
		return nil, fmt.Errorf("transfer: 未找到任务 %q", prefix)
	}
	return hit, nil
}

// ── 清单构建（发送端）────────────────────────────────────────

// buildManifest 展开文件/文件夹为签名清单，流式计算 SHA-256
func buildManifest(paths []string) ([]protocol.FileEntry, map[int]string, int64, error) {
	var entries []protocol.FileEntry
	abs := make(map[int]string)
	var total int64

	addFile := func(absPath, rel string, size int64) error {
		sum, err := hashFile(absPath)
		if err != nil {
			return err
		}
		abs[len(entries)] = absPath
		entries = append(entries, protocol.FileEntry{Rel: rel, Size: size, SHA256: sum})
		total += size
		return nil
	}

	for _, p := range paths {
		ap, err := filepath.Abs(p)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("transfer: 路径 %q 无效: %w", p, err)
		}
		st, err := os.Stat(ap)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("transfer: 无法访问 %q: %w", p, err)
		}
		if !st.IsDir() {
			if err := addFile(ap, filepath.Base(ap), st.Size()); err != nil {
				return nil, nil, 0, err
			}
			continue
		}
		base := filepath.Base(ap)
		err = filepath.WalkDir(ap, func(path string, d fs.DirEntry, werr error) error {
			if werr != nil {
				return werr
			}
			relInside, err := filepath.Rel(ap, path)
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(filepath.Join(base, relInside))
			if d.IsDir() {
				if relInside != "." {
					entries = append(entries, protocol.FileEntry{Rel: rel, IsDir: true})
				}
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			return addFile(path, rel, info.Size())
		})
		if err != nil {
			return nil, nil, 0, fmt.Errorf("transfer: 遍历目录 %q 失败: %w", p, err)
		}
	}
	if len(entries) == 0 {
		return nil, nil, 0, errors.New("transfer: 没有可发送的文件")
	}
	return entries, abs, total, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("transfer: 打开 %q 失败: %w", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("transfer: 计算哈希失败 %q: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ── 工具 ────────────────────────────────────────────────────

func newToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// addDone 累加进度并计算速率、节流发布进度事件
func (s *Service) addDone(t *Task, n int, file string) {
	t.mu.Lock()
	t.done += int64(n)
	now := time.Now()
	if t.lastTime.IsZero() {
		t.lastTime, t.lastBytes = now, t.done
	} else if dt := now.Sub(t.lastTime); dt >= time.Second {
		t.speed = float64(t.done-t.lastBytes) / dt.Seconds()
		t.lastTime, t.lastBytes = now, t.done
	}
	emit := now.Sub(t.lastEvent) >= progressGap
	if emit {
		t.lastEvent = now
	}
	ev := ProgressEvent{TaskID: t.ID, PeerName: t.PeerName, File: file, Done: t.done, Total: t.Total, SpeedBps: t.speed}
	t.mu.Unlock()
	if emit {
		s.b.Publish(bus.TopicTransferProgress, ev)
	}
}

func (s *Service) setState(t *Task, st State) {
	t.mu.Lock()
	t.State = st
	t.mu.Unlock()
}
