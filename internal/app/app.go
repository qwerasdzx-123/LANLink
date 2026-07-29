// Package app 应用装配层：按依赖顺序组装各模块并管理生命周期。
// UI（CLI/Wails）只与 App 和事件总线交互，不直接触碰 socket。
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"lanlink/internal/bus"
	"lanlink/internal/chat"
	"lanlink/internal/discovery"
	"lanlink/internal/identity"
	"lanlink/internal/secure"
	"lanlink/internal/transfer"
	"lanlink/internal/transport"
)

// Config 应用配置
type Config struct {
	Name      string // 显示昵称
	UDPPort   int    // 信令端口，默认 45450
	TCPPort   int    // 数据端口，默认 45451
	DataDir   string // 数据目录（身份/历史/群组）
	Downloads string // 下载目录
}

// Default 默认配置
func Default() Config {
	home, _ := os.UserHomeDir()
	host, _ := os.Hostname()
	if host == "" {
		host = "user"
	}
	return Config{
		Name:      host,
		UDPPort:   45450,
		TCPPort:   45451,
		DataDir:   filepath.Join(home, ".lanlink"),
		Downloads: filepath.Join(home, "Downloads", "LanLink"),
	}
}

// App 应用根对象
type App struct {
	Cfg      Config
	Bus      *bus.Bus
	ID       *identity.Identity
	Disc     *discovery.Discovery
	Chat     *chat.Service
	Transfer *transfer.Service

	udp    *transport.UDP
	tcpSrv *transport.TCPServer
	cancel context.CancelFunc
}

// New 组装全部模块（依赖单向向下：app → 服务层 → 核心层 → 基础层）
func New(cfg Config) (*App, error) {
	id, err := identity.LoadOrCreate(cfg.DataDir, cfg.Name)
	if err != nil {
		return nil, err
	}
	b := bus.New()

	udp, err := transport.NewUDP(cfg.UDPPort)
	if err != nil {
		return nil, err
	}
	tcpSrv, err := transport.ListenTCP(cfg.TCPPort)
	if err != nil {
		return nil, err
	}

	disc := discovery.New(id, udp, b, tcpSrv.Port)
	sec := secure.NewManager(id, udp, disc)

	chatSvc, err := chat.New(id, udp, disc, sec, b, cfg.DataDir)
	if err != nil {
		return nil, err
	}
	xferSvc, err := transfer.New(id, udp, tcpSrv, disc, sec, b, cfg.Downloads)
	if err != nil {
		return nil, err
	}

	return &App{
		Cfg: cfg, Bus: b, ID: id,
		Disc: disc, Chat: chatSvc, Transfer: xferSvc,
		udp: udp, tcpSrv: tcpSrv,
	}, nil
}

// Start 启动全部后台协程
func (a *App) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.udp.Start(ctx)
	a.Disc.Start(ctx)
	a.Chat.Start(ctx)
	a.Transfer.Start(ctx)
}

// Stop 优雅退出：广播下线 → 取消根 context
func (a *App) Stop() {
	a.Disc.Stop()
	if a.cancel != nil {
		a.cancel()
	}
}

// Summary 启动摘要
func (a *App) Summary() string {
	return fmt.Sprintf("节点 %s (%s)  UDP:%d  TCP:%d  下载目录: %s",
		a.ID.Name(), a.ID.ID, a.udp.Port, a.tcpSrv.Port, a.Cfg.Downloads)
}
