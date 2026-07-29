package transfer

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	"lanlink/internal/bus"
	"lanlink/internal/protocol"
	"lanlink/internal/transport"
)

// SendPaths 发起文件传输：构建清单 → 可靠发送 FILE_OFFER → 等待对方接受后由对方连入拉取
func (s *Service) SendPaths(ctx context.Context, peerID string, paths []string) (string, error) {
	peer, ok := s.disc.Get(peerID)
	if !ok || !peer.Online {
		return "", fmt.Errorf("transfer: 对端离线，无法发起传输")
	}

	files, absPaths, total, err := buildManifest(paths)
	if err != nil {
		return "", err
	}

	task := &Task{
		ID:       newToken()[:12],
		PeerID:   peerID,
		PeerName: peer.Name,
		Dir:      DirSend,
		Files:    files,
		Token:    newToken(),
		Total:    total,
		State:    StateOffering,
		absPaths: absPaths,
	}

	offer := protocol.FileOffer{
		TaskID: task.ID, From: s.id.ID, FromName: s.id.Name(),
		Token: task.Token, TCPPort: s.tcp.Port,
		Files: files, Total: total,
	}
	f, err := protocol.NewFrame(protocol.TFileOffer, &offer)
	if err != nil {
		return "", err
	}
	if len(f.Payload) > protocol.MaxUDPPayload {
		return "", fmt.Errorf("transfer: 文件清单过大（%d 项），请打包压缩后再发送", len(files))
	}

	s.tmu.Lock()
	s.tasks[task.ID] = task
	s.tmu.Unlock()

	addr, _ := s.disc.PeerUDPAddr(peerID)
	if err := s.udp.SendReliable(ctx, addr, f); err != nil {
		s.setState(task, StateFailed)
		return "", fmt.Errorf("transfer: 发送传输请求失败: %w", err)
	}
	return task.ID, nil
}

// handleAnswer 收到接收方的接受/拒绝应答
func (s *Service) handleAnswer(f *protocol.Frame, _ *net.UDPAddr) {
	var ans protocol.FileAnswer
	if err := protocol.DecodePayload(f, &ans); err != nil {
		return
	}
	s.tmu.RLock()
	task, ok := s.tasks[ans.TaskID]
	s.tmu.RUnlock()
	if !ok || task.Dir != DirSend || task.PeerID != ans.From {
		return
	}
	if ans.Accept {
		log.Printf("[transfer] 对方已接受任务 %s，等待其连入拉取", task.ID)
		return
	}
	s.setState(task, StateRejected)
	reason := ans.Reason
	if reason == "" {
		reason = "对方拒绝接收"
	}
	s.b.Publish(bus.TopicTransferError, ResultEvent{TaskID: task.ID, PeerName: task.PeerName, Message: reason})
}

// handleConn 发送端 TCP 服务：HELLO 绑定 → 响应 PULL / RATE / DONE
func (s *Service) handleConn(fc *transport.FrameConn) {
	hf, err := fc.Read(10 * time.Second)
	if err != nil || hf.Type != protocol.THello {
		return
	}
	var hello protocol.Hello
	if err := protocol.DecodePayload(hf, &hello); err != nil {
		return
	}

	s.tmu.RLock()
	task, ok := s.tasks[hello.TaskID]
	s.tmu.RUnlock()
	if !ok || task.Dir != DirSend || task.Token != hello.Token || task.PeerID != hello.From {
		s.writeErr(fc, 403, "任务不存在或凭证无效")
		log.Printf("[transfer] 拒绝非法 TCP 搭线: %s", fc.RemoteAddr())
		return
	}
	s.setState(task, StateTransferring)

	for {
		f, err := fc.Read(streamRWTime)
		if err != nil {
			// 连接断开：标记暂停（接收方重连后可从断点继续）
			task.mu.Lock()
			if task.State == StateTransferring {
				task.State = StatePaused
			}
			task.mu.Unlock()
			return
		}
		switch f.Type {
		case protocol.TPull:
			var pull protocol.Pull
			if err := protocol.DecodePayload(f, &pull); err != nil {
				s.writeErr(fc, 400, "PULL 载荷非法")
				return
			}
			if err := s.streamFile(task, fc, pull); err != nil {
				log.Printf("[transfer] 发送文件失败: %v", err)
				return
			}
		case protocol.TRate:
			var rc protocol.RateCtl
			if protocol.DecodePayload(f, &rc) == nil {
				s.SetRateKBps(int(rc.BytesPerSec / 1024)) // 接收端反馈调速
			}
		case protocol.TTaskDone:
			s.setState(task, StateCompleted)
			s.b.Publish(bus.TopicTransferDone, ResultEvent{
				TaskID: task.ID, PeerName: task.PeerName,
				Message: fmt.Sprintf("发送完成（%d 个文件，%s）", len(task.Files), humanBytes(task.Total)),
			})
			return
		default:
			return
		}
	}
}

// streamFile 从指定偏移流式发送单个文件（限速 + 缓冲池 + 可选加密）
func (s *Service) streamFile(task *Task, fc *transport.FrameConn, pull protocol.Pull) error {
	if pull.Index < 0 || pull.Index >= len(task.Files) {
		s.writeErr(fc, 400, "文件下标越界")
		return fmt.Errorf("下标越界: %d", pull.Index)
	}
	entry := task.Files[pull.Index]
	if entry.IsDir {
		return s.writeFrame(fc, protocol.TFileEnd, &protocol.FileEnd{Index: pull.Index, SHA256: ""})
	}
	absPath, ok := task.absPaths[pull.Index]
	if !ok {
		s.writeErr(fc, 404, "文件不存在")
		return fmt.Errorf("清单缺少路径: %d", pull.Index)
	}

	fh, err := os.Open(absPath)
	if err != nil {
		s.writeErr(fc, 404, "源文件已不可读: "+entry.Rel)
		return fmt.Errorf("打开 %q: %w", absPath, err)
	}
	defer fh.Close()

	// 防拼坏文件：源文件在传输前被修改则拒绝续传
	if st, err := fh.Stat(); err != nil || st.Size() != entry.Size {
		s.writeErr(fc, 409, "源文件已变更，请重新发起传输: "+entry.Rel)
		return fmt.Errorf("源文件 %q 已变更", absPath)
	}
	if pull.Offset > 0 {
		if _, err := fh.Seek(pull.Offset, io.SeekStart); err != nil {
			s.writeErr(fc, 500, "定位偏移失败")
			return err
		}
	}

	bufp := s.pool.Get().(*[]byte)
	defer s.pool.Put(bufp)
	buf := *bufp

	offset := pull.Offset
	encrypted := s.sec.Has(task.PeerID)
	ctx := context.Background()

	for {
		n, rerr := fh.Read(buf)
		if n > 0 {
			if err := s.limiter.WaitN(ctx, n); err != nil {
				return err
			}
			data := buf[:n]
			flags := byte(0)
			if encrypted {
				sealed, err := s.sec.Seal(task.PeerID, data)
				if err != nil {
					return fmt.Errorf("加密数据块失败: %w", err)
				}
				data = sealed
				flags = protocol.FlagEnc
			}
			cf, err := protocol.NewFrame(protocol.TChunk, &protocol.Chunk{Index: pull.Index, Offset: offset, Data: data})
			if err != nil {
				return err
			}
			cf.Flags |= flags
			if err := fc.Write(cf, streamRWTime); err != nil {
				return fmt.Errorf("写数据块失败: %w", err)
			}
			offset += int64(n)
			s.addDone(task, n, entry.Rel)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			s.writeErr(fc, 500, "读源文件失败")
			return fmt.Errorf("读 %q: %w", absPath, rerr)
		}
	}
	return s.writeFrame(fc, protocol.TFileEnd, &protocol.FileEnd{Index: pull.Index, SHA256: entry.SHA256})
}

func (s *Service) writeFrame(fc *transport.FrameConn, typ uint16, v any) error {
	f, err := protocol.NewFrame(typ, v)
	if err != nil {
		return err
	}
	return fc.Write(f, streamRWTime)
}

func (s *Service) writeErr(fc *transport.FrameConn, code int, msg string) {
	_ = s.writeFrame(fc, protocol.TErr, &protocol.ErrMsg{Code: code, Msg: msg})
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
