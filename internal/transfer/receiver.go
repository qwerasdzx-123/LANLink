package transfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"lanlink/internal/bus"
	"lanlink/internal/protocol"
	"lanlink/internal/transport"
)

// resumeMeta 续传元数据（.llmeta），三要素校验防拼坏文件
type resumeMeta struct {
	TaskID string `json:"task_id"`
	Sender string `json:"sender"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Offset int64  `json:"offset"`
}

// handleOffer 收到文件传输请求
func (s *Service) handleOffer(f *protocol.Frame, from *net.UDPAddr) {
	var offer protocol.FileOffer
	if err := protocol.DecodePayload(f, &offer); err != nil {
		return
	}
	if _, ok := s.disc.Get(offer.From); !ok {
		log.Printf("[transfer] 忽略未知节点 %s 的传输请求", offer.From)
		return
	}
	s.tmu.Lock()
	if _, dup := s.tasks[offer.TaskID]; dup {
		s.tmu.Unlock()
		return // 重复 OFFER（UDP 重传），忽略
	}
	task := &Task{
		ID: offer.TaskID, PeerID: offer.From, PeerName: offer.FromName,
		Dir: DirRecv, Files: offer.Files, Token: offer.Token,
		Total: offer.Total, State: StatePendingLocal,
		senderAddr: fmt.Sprintf("%s:%d", from.IP, offer.TCPPort),
	}
	s.tasks[task.ID] = task
	s.tmu.Unlock()

	s.b.Publish(bus.TopicTransferOffer, OfferEvent{
		TaskID: task.ID, PeerName: task.PeerName,
		Files: len(task.Files), Total: task.Total,
	})
}

// Accept 接受任务并开始（或继续）拉取
func (s *Service) Accept(ctx context.Context, taskID string) error {
	task, err := s.FindTask(taskID)
	if err != nil {
		return err
	}
	if task.Dir != DirRecv {
		return fmt.Errorf("transfer: %s 是发送任务", task.ID)
	}
	task.mu.Lock()
	if task.State == StateTransferring {
		task.mu.Unlock()
		return fmt.Errorf("transfer: 任务 %s 正在传输中", task.ID)
	}
	task.pausing = false
	task.mu.Unlock()

	// 通知发送方"已接受"
	ans := protocol.FileAnswer{TaskID: task.ID, From: s.id.ID, Accept: true}
	if af, err := protocol.NewFrame(protocol.TFileAns, &ans); err == nil {
		if addr, ok := s.disc.PeerUDPAddr(task.PeerID); ok {
			if err := s.udp.SendReliable(ctx, addr, af); err != nil {
				log.Printf("[transfer] 发送接受应答失败（继续尝试直连）: %v", err)
			}
		}
	}

	pullCtx, cancel := context.WithCancel(context.Background())
	task.mu.Lock()
	task.cancel = cancel
	task.mu.Unlock()
	go s.runPull(pullCtx, task)
	return nil
}

// Reject 拒绝任务
func (s *Service) Reject(ctx context.Context, taskID, reason string) error {
	task, err := s.FindTask(taskID)
	if err != nil {
		return err
	}
	ans := protocol.FileAnswer{TaskID: task.ID, From: s.id.ID, Accept: false, Reason: reason}
	af, err := protocol.NewFrame(protocol.TFileAns, &ans)
	if err != nil {
		return err
	}
	if addr, ok := s.disc.PeerUDPAddr(task.PeerID); ok {
		_ = s.udp.SendReliable(ctx, addr, af)
	}
	s.setState(task, StateRejected)
	return nil
}

// Pause 暂停接收（断点已持久化，可随时 Resume）
func (s *Service) Pause(taskID string) error {
	task, err := s.FindTask(taskID)
	if err != nil {
		return err
	}
	task.mu.Lock()
	defer task.mu.Unlock()
	if task.State != StateTransferring || task.cancel == nil {
		return fmt.Errorf("transfer: 任务 %s 当前不在传输中", task.ID)
	}
	task.pausing = true
	task.cancel()
	return nil
}

// Resume 断点续传
func (s *Service) Resume(ctx context.Context, taskID string) error {
	task, err := s.FindTask(taskID)
	if err != nil {
		return err
	}
	task.mu.Lock()
	st := task.State
	task.mu.Unlock()
	if st != StatePaused && st != StateFailed {
		return fmt.Errorf("transfer: 任务 %s 状态为 %s，无需续传", task.ID, st)
	}
	return s.Accept(ctx, task.ID)
}

// runPull 接收主流程：连接 → HELLO → 逐文件拉取 → TASK_DONE
func (s *Service) runPull(ctx context.Context, task *Task) {
	s.setState(task, StateTransferring)

	fail := func(err error) {
		task.mu.Lock()
		paused := task.pausing
		task.mu.Unlock()
		if paused || ctx.Err() != nil {
			s.setState(task, StatePaused)
			s.b.Publish(bus.TopicTransferPaused, ResultEvent{TaskID: task.ID, PeerName: task.PeerName, Message: "已暂停，断点已保存"})
			return
		}
		s.setState(task, StatePaused) // 网络闪断也按 PAUSED 处理，等待续传
		s.b.Publish(bus.TopicTransferError, ResultEvent{TaskID: task.ID, PeerName: task.PeerName,
			Message: fmt.Sprintf("传输中断（可用 resume 续传）: %v", err)})
	}

	fc, err := transport.DialTCP(ctx, task.senderAddr)
	if err != nil {
		fail(err)
		return
	}
	defer fc.Close()
	go func() { // ctx 取消 → 关闭连接解除读阻塞
		<-ctx.Done()
		fc.Close()
	}()

	hello := protocol.Hello{From: s.id.ID, TaskID: task.ID, Token: task.Token}
	if err := s.writeFrame2(fc, protocol.THello, &hello); err != nil {
		fail(fmt.Errorf("HELLO 失败: %w", err))
		return
	}

	// 重算已完成字节（续传场景进度归位）
	task.mu.Lock()
	task.done = 0
	task.mu.Unlock()

	dlRoot := s.Downloads()
	for i, entry := range task.Files {
		if entry.IsDir {
			dir := filepath.Join(dlRoot, filepath.FromSlash(entry.Rel))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				fail(fmt.Errorf("创建目录 %q 失败: %w", entry.Rel, err))
				return
			}
			continue
		}
		if err := s.pullFile(ctx, fc, task, i); err != nil {
			fail(err)
			return
		}
	}

	_ = s.writeFrame2(fc, protocol.TTaskDone, nil)
	s.setState(task, StateCompleted)
	s.b.Publish(bus.TopicTransferDone, ResultEvent{
		TaskID: task.ID, PeerName: task.PeerName,
		Message: fmt.Sprintf("接收完成（%d 个文件，%s）→ %s", len(task.Files), humanBytes(task.Total), s.Downloads()),
	})
}

// pullFile 拉取单个文件：续传定位 → PULL → 收块写盘 → 哈希校验 → 落定
func (s *Service) pullFile(ctx context.Context, fc *transport.FrameConn, task *Task, idx int) error {
	entry := task.Files[idx]
	dlRoot := s.Downloads()
	dst := filepath.Join(dlRoot, filepath.FromSlash(entry.Rel))
	if !strings.HasPrefix(filepath.Clean(dst), filepath.Clean(dlRoot)) {
		return fmt.Errorf("非法路径（路径穿越攻击?）: %q", entry.Rel)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("创建目录失败: %w", err)
	}

	// 目标已存在且哈希一致 → 秒传跳过
	if st, err := os.Stat(dst); err == nil && st.Size() == entry.Size {
		if sum, err := hashFile(dst); err == nil && sum == entry.SHA256 {
			s.addDone(task, int(entry.Size), entry.Rel)
			return nil
		}
	}

	part := dst + ".part"
	metaPath := dst + ".llmeta"
	offset, hasher, err := s.prepareResume(part, metaPath, task, entry)
	if err != nil {
		return err
	}
	if offset > 0 {
		s.addDone(task, int(offset), entry.Rel)
	}

	fh, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("打开临时文件失败: %w", err)
	}
	defer fh.Close()
	if _, err := fh.Seek(offset, io.SeekStart); err != nil {
		return fmt.Errorf("定位偏移失败: %w", err)
	}

	if err := s.writeFrame2(fc, protocol.TPull, &protocol.Pull{Index: idx, Offset: offset}); err != nil {
		return fmt.Errorf("发送 PULL 失败: %w", err)
	}

	lastMeta := offset
	for {
		if ctx.Err() != nil {
			s.saveMeta(metaPath, task, entry, offset)
			return ctx.Err()
		}
		f, err := fc.Read(streamRWTime)
		if err != nil {
			s.saveMeta(metaPath, task, entry, offset)
			return fmt.Errorf("读数据帧失败: %w", err)
		}
		switch f.Type {
		case protocol.TChunk:
			var ch protocol.Chunk
			if err := protocol.DecodePayload(f, &ch); err != nil {
				return err
			}
			data := ch.Data
			if f.Flags&protocol.FlagEnc != 0 {
				plain, err := s.sec.Open(task.PeerID, data)
				if err != nil {
					return fmt.Errorf("解密数据块失败: %w", err)
				}
				data = plain
			}
			if ch.Index != idx || ch.Offset != offset {
				return fmt.Errorf("数据块乱序: 期望 %d@%d 实收 %d@%d", idx, offset, ch.Index, ch.Offset)
			}
			if _, err := fh.Write(data); err != nil {
				return fmt.Errorf("写盘失败: %w", err)
			}
			hasher.Write(data)
			offset += int64(len(data))
			if offset-lastMeta >= metaEvery { // 周期性持久化续传位点
				s.saveMeta(metaPath, task, entry, offset)
				lastMeta = offset
			}
			s.addDone(task, len(data), entry.Rel)

		case protocol.TFileEnd:
			var fe protocol.FileEnd
			if err := protocol.DecodePayload(f, &fe); err != nil {
				return err
			}
			got := hex.EncodeToString(hasher.Sum(nil))
			if got != entry.SHA256 {
				fh.Close()
				_ = os.Remove(part)
				_ = os.Remove(metaPath)
				return fmt.Errorf("文件 %q 哈希校验失败（数据损坏，已删除临时文件）", entry.Rel)
			}
			fh.Close()
			if err := os.Rename(part, dst); err != nil {
				return fmt.Errorf("落定文件失败: %w", err)
			}
			_ = os.Remove(metaPath)
			return nil

		case protocol.TErr:
			var em protocol.ErrMsg
			_ = protocol.DecodePayload(f, &em)
			s.saveMeta(metaPath, task, entry, offset)
			return fmt.Errorf("发送方错误 [%d]: %s", em.Code, em.Msg)

		default:
			return fmt.Errorf("意外帧类型: 0x%04X", f.Type)
		}
	}
}

// prepareResume 校验续传条件：meta 的 size/sha256/sender 三要素均匹配才续传，
// 并对已有部分重算哈希（保证最终 SHA-256 覆盖全文件）。
func (s *Service) prepareResume(part, metaPath string, task *Task, entry protocol.FileEntry) (int64, hash.Hash, error) {
	h := sha256.New()

	mb, err := os.ReadFile(metaPath)
	if err != nil {
		return 0, h, nil // 无 meta，从头开始
	}
	var meta resumeMeta
	if json.Unmarshal(mb, &meta) != nil ||
		meta.Size != entry.Size || meta.SHA256 != entry.SHA256 || meta.Sender != task.PeerID {
		// 三要素不匹配：源文件已变或换了发送者，废弃断点
		_ = os.Remove(part)
		_ = os.Remove(metaPath)
		return 0, h, nil
	}
	st, err := os.Stat(part)
	if err != nil {
		return 0, h, nil
	}
	offset := meta.Offset
	if st.Size() < offset {
		offset = st.Size() // .part 比位点短（写盘中断），以实际为准
	}
	if offset <= 0 {
		return 0, h, nil
	}

	// 重算已有部分哈希
	pf, err := os.Open(part)
	if err != nil {
		return 0, sha256.New(), nil
	}
	defer pf.Close()
	if _, err := io.CopyN(h, pf, offset); err != nil {
		return 0, sha256.New(), nil
	}
	log.Printf("[transfer] %q 从断点 %s 续传", entry.Rel, humanBytes(offset))
	return offset, h, nil
}

func (s *Service) saveMeta(metaPath string, task *Task, entry protocol.FileEntry, offset int64) {
	meta := resumeMeta{
		TaskID: task.ID, Sender: task.PeerID,
		Size: entry.Size, SHA256: entry.SHA256, Offset: offset,
	}
	b, _ := json.Marshal(meta)
	if err := os.WriteFile(metaPath, b, 0o644); err != nil {
		log.Printf("[transfer] 保存续传位点失败: %v", err)
	}
}

// RequestRate 接收端反馈调速（0 = 请求不限速）——需任务正在传输时由 T_RATE 帧携带；
// 简化实现：直接调整本地（发送任务）限速器。
func (s *Service) writeFrame2(fc *transport.FrameConn, typ uint16, v any) error {
	f, err := protocol.NewFrame(typ, v)
	if err != nil {
		return err
	}
	return fc.Write(f, streamRWTime)
}
