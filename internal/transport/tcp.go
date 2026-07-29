package transport

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"lanlink/internal/protocol"
)

// DefaultIOTimeout TCP 帧读写默认超时
const DefaultIOTimeout = 30 * time.Second

// FrameConn 面向帧的 TCP 连接（TCP_NODELAY，带超时）
type FrameConn struct {
	c  net.Conn
	br *bufio.Reader
	wl sync.Mutex // 串行化写，允许多协程安全写帧
}

func newFrameConn(c net.Conn) *FrameConn {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true) // 禁用 Nagle：信令小帧低延迟
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(15 * time.Second)
	}
	return &FrameConn{c: c, br: bufio.NewReaderSize(c, 1<<20)}
}

// Read 读取一帧（超时保护，杜绝无限阻塞）
func (fc *FrameConn) Read(timeout time.Duration) (*protocol.Frame, error) {
	if timeout <= 0 {
		timeout = DefaultIOTimeout
	}
	if err := fc.c.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	return protocol.ReadFrame(fc.br)
}

// Write 写入一帧
func (fc *FrameConn) Write(f *protocol.Frame, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = DefaultIOTimeout
	}
	fc.wl.Lock()
	defer fc.wl.Unlock()
	if err := fc.c.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	return protocol.WriteFrame(fc.c, f)
}

func (fc *FrameConn) Close() error         { return fc.c.Close() }
func (fc *FrameConn) RemoteAddr() net.Addr { return fc.c.RemoteAddr() }

// TCPServer 数据通道监听器
type TCPServer struct {
	ln   net.Listener
	Port int
}

// ListenTCP 监听指定端口（0 = 系统分配）
func ListenTCP(port int) (*TCPServer, error) {
	ln, err := net.Listen("tcp4", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, fmt.Errorf("transport: TCP 端口 %d 监听失败: %w", port, err)
	}
	return &TCPServer{ln: ln, Port: ln.Addr().(*net.TCPAddr).Port}, nil
}

// Serve 接受连接并交给 handler（每连接一个协程）
func (s *TCPServer) Serve(ctx context.Context, handler func(*FrameConn)) {
	go func() {
		<-ctx.Done()
		s.ln.Close()
	}()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[transport] TCP accept 错误: %v", err)
			continue
		}
		fc := newFrameConn(conn)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[transport] TCP 处理 panic: %v", r)
				}
				fc.Close()
			}()
			handler(fc)
		}()
	}
}

// DialTCP 建立到对端数据通道的连接
func DialTCP(ctx context.Context, addr string) (*FrameConn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp4", addr)
	if err != nil {
		return nil, fmt.Errorf("transport: 连接 %s 失败: %w", addr, err)
	}
	return newFrameConn(conn), nil
}
