// lanlink-gui 局域网通联工具的原生图形界面（基于 Fyne，QQ 式三栏布局 + mac 风格主题）。
// 左栏：在线用户列表；中栏：聊天气泡 + 输入区（支持文件按钮与拖拽发送）；
// 右栏：对方资料面板，文件传输时自动切换为传输进度面板。
// 支持：设置面板、明暗主题切换、托盘最小化、消息闪烁/弹出提醒、提示音、备注等。
//
// 编译需 CGO（gcc/MinGW-w64）：
//
//	go build -ldflags "-H windowsgui" -o lanlink-gui.exe ./cmd/lanlink-gui
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image/color"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	fyneApp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"lanlink/internal/app"
	"lanlink/internal/bus"
	"lanlink/internal/chat"
	"lanlink/internal/discovery"
	"lanlink/internal/identity"
	"lanlink/internal/transfer"
)

const (
	broadcastKey = "_broadcast"
	groupPrefix  = "group:"
	winTitle     = "LanLink 局域网通联工具"
)

// ── 数据模型 ────────────────────────────────────────────────

// leftRow 左栏一行（广播/群/用户）
type leftRow struct {
	key    string // 会话键：_broadcast / group:xx / peerID
	title  string
	sub    string
	online bool
	isPeer bool
}

// msgItem 一条聊天消息
type msgItem struct {
	sender string
	text   string
	ts     int64
	mine   bool
}

// gui 图形界面控制器
type gui struct {
	app     *app.App
	fa      fyne.App
	win     fyne.Window
	set     *settings
	dataDir string

	mu       sync.Mutex
	peers    []discovery.Peer
	leftRows []leftRow
	convs    map[string][]msgItem
	curKey   string
	curTitle string

	// 左栏
	peersList *widget.List
	meAvatar  *canvas.Image
	meName    *widget.Label

	// 中栏
	chatTitle *widget.Label
	chatBg    *canvas.Rectangle
	msgBox    *fyne.Container
	msgScroll *container.Scroll
	entry     *widget.Entry

	// 右栏
	rightContent *fyne.Container
	infoBox      *fyne.Container
	taskPanel    fyne.CanvasObject
	taskBox      *fyne.Container
	rateEntry    *widget.Entry
	showTasks    bool
	prevActive   int

	statusLabel *widget.Label
}

func main() {
	initCrashLog()
	def := app.Default()
	name := flag.String("name", def.Name, "显示昵称")
	udpPort := flag.Int("udp", def.UDPPort, "UDP 信令端口")
	tcpPort := flag.Int("tcp", def.TCPPort, "TCP 数据端口(0=自动)")
	dataDir := flag.String("data", def.DataDir, "数据目录")
	dlDir := flag.String("dl", def.Downloads, "下载目录")
	flag.Parse()

	set := loadSettings(*dataDir)
	cfg := app.Config{
		Name: *name, UDPPort: *udpPort, TCPPort: *tcpPort,
		DataDir: *dataDir, Downloads: *dlDir,
	}
	if set.Nickname != "" {
		cfg.Name = set.Nickname
	}
	if set.Downloads != "" {
		cfg.Downloads = set.Downloads
	}

	a, err := app.New(cfg)
	if err != nil {
		fmt.Println("启动失败:", err)
		return
	}
	a.Start()

	fa := fyneApp.NewWithID("lanlink-gui")
	fa.Settings().SetTheme(macTheme{dark: set.Dark})
	fa.SetIcon(appIcon)
	w := fa.NewWindow(winTitle)
	w.SetIcon(appIcon)

	g := &gui{app: a, fa: fa, win: w, set: set, dataDir: *dataDir,
		convs: make(map[string][]msgItem)}
	g.build()
	g.setupTray()
	w.Resize(fyne.NewSize(1180, 760))
	w.SetOnDropped(g.onDropped)
	w.SetCloseIntercept(func() {
		if g.set.CloseToTray {
			w.Hide()
		} else {
			a.Stop()
			fa.Quit()
		}
	})
	g.start()
	g.note("已启动，正在发现局域网节点…")
	w.ShowAndRun()
}

// setupTray 系统托盘：图标 + 菜单（显示/退出）
func (g *gui) setupTray() {
	desk, ok := g.fa.(desktop.App)
	if !ok {
		return
	}
	menu := fyne.NewMenu("LanLink",
		fyne.NewMenuItem("显示主窗口", func() {
			g.win.Show()
			g.win.RequestFocus()
		}),
		fyne.NewMenuItem("退出", func() {
			g.app.Stop()
			g.fa.Quit()
		}),
	)
	desk.SetSystemTrayMenu(menu)
	desk.SetSystemTrayIcon(appIcon)
}

// ── 布局构建 ────────────────────────────────────────────────

func (g *gui) build() {
	g.buildLeft()
	g.buildMiddle()
	g.buildRight()

	left := g.leftColumn()
	right := g.rightColumn()
	middle := g.middleColumn()

	g.win.SetContent(container.NewBorder(nil, nil, left, right, middle))
	g.openConv(broadcastKey, "广播")
}

// 左栏：我的资料 + 在线用户列表 + 工具栏
func (g *gui) buildLeft() {
	g.meAvatar = canvas.NewImageFromResource(appIcon)
	g.meAvatar.FillMode = canvas.ImageFillContain
	g.meAvatar.SetMinSize(fyne.NewSize(44, 44))
	g.meName = widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	g.refreshMe()

	g.peersList = widget.NewList(
		func() int {
			g.mu.Lock()
			defer g.mu.Unlock()
			return len(g.leftRows)
		},
		func() fyne.CanvasObject {
			title := widget.NewLabelWithStyle("模板", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			sub := widget.NewLabel("模板")
			return container.NewVBox(title, sub)
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			g.mu.Lock()
			if id < 0 || id >= len(g.leftRows) {
				g.mu.Unlock()
				return
			}
			r := g.leftRows[id]
			g.mu.Unlock()
			box := o.(*fyne.Container)
			title := box.Objects[0].(*widget.Label)
			sub := box.Objects[1].(*widget.Label)
			dot := ""
			if r.isPeer {
				if r.online {
					dot = "🟢 "
				} else {
					dot = "⚪ "
				}
			}
			title.SetText(dot + r.title)
			sub.SetText(r.sub)
		},
	)
	g.peersList.OnSelected = func(id widget.ListItemID) {
		g.mu.Lock()
		if id < 0 || id >= len(g.leftRows) {
			g.mu.Unlock()
			return
		}
		r := g.leftRows[id]
		g.mu.Unlock()
		g.openConv(r.key, r.title)
	}
	g.rebuildLeftRows()
}

// refreshMe 刷新左栏顶部本人资料（头像 + 昵称）
func (g *gui) refreshMe() {
	if g.set.Avatar != "" {
		if _, err := os.Stat(g.set.Avatar); err == nil {
			g.meAvatar.File = g.set.Avatar
			g.meAvatar.Resource = nil
		}
	} else {
		g.meAvatar.File = ""
		g.meAvatar.Resource = appIcon
	}
	g.meAvatar.Refresh()
	g.meName.SetText(g.app.ID.Name())
}

func (g *gui) leftColumn() fyne.CanvasObject {
	me := container.NewBorder(nil, nil, g.meAvatar, nil, g.meName)

	settingsBtn := widget.NewButtonWithIcon("设置", theme.SettingsIcon(), g.showSettings)
	themeBtn := widget.NewButtonWithIcon("主题", theme.ColorPaletteIcon(), g.toggleTheme)
	newGroup := widget.NewButtonWithIcon("建群", theme.ContentAddIcon(), g.newGroup)
	tools := container.NewGridWithColumns(3, settingsBtn, themeBtn, newGroup)

	width := canvas.NewRectangle(color.Transparent)
	width.SetMinSize(fyne.NewSize(240, 1))
	return container.NewBorder(
		container.NewVBox(width, me, widget.NewSeparator()),
		tools, nil, nil, g.peersList,
	)
}

// 中栏：聊天气泡 + 输入区
func (g *gui) buildMiddle() {
	g.chatTitle = widget.NewLabelWithStyle("", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	g.statusLabel = widget.NewLabel("")
	g.statusLabel.Truncation = fyne.TextTruncateEllipsis

	g.msgBox = container.NewVBox()
	g.msgScroll = container.NewVScroll(container.NewPadded(g.msgBox))
	g.chatBg = canvas.NewRectangle(chatBgColor(g.set.Dark))

	g.entry = widget.NewEntry()
	g.entry.SetPlaceHolder("输入消息，回车发送；文件可直接拖入窗口")
	g.entry.OnSubmitted = func(string) { g.sendCurrent() }
}

func (g *gui) middleColumn() fyne.CanvasObject {
	fileBtn := widget.NewButtonWithIcon("", theme.FileIcon(), func() { g.pickAndSend(false) })
	dirBtn := widget.NewButtonWithIcon("", theme.FolderOpenIcon(), func() { g.pickAndSend(true) })
	sendBtn := widget.NewButtonWithIcon("发送", theme.MailSendIcon(), g.sendCurrent)
	input := container.NewBorder(nil, nil, container.NewHBox(fileBtn, dirBtn), sendBtn, g.entry)

	chatArea := container.NewStack(g.chatBg, g.msgScroll)

	top := container.NewVBox(g.chatTitle, widget.NewSeparator())
	bottom := container.NewVBox(widget.NewSeparator(), input, g.statusLabel)
	return container.NewBorder(top, bottom, nil, nil, chatArea)
}

// 右栏：对方资料 / 传输进度
func (g *gui) buildRight() {
	g.infoBox = container.NewVBox()

	g.rateEntry = widget.NewEntry()
	g.rateEntry.SetPlaceHolder("限速 KB/s（0=不限）")
	applyRate := widget.NewButton("应用限速", g.applyRate)
	g.taskBox = container.NewVBox()
	g.taskPanel = container.NewBorder(
		container.NewVBox(
			widget.NewLabelWithStyle("文件传输", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
			container.NewBorder(nil, nil, nil, applyRate, g.rateEntry),
			widget.NewSeparator(),
		),
		nil, nil, nil,
		container.NewVScroll(g.taskBox),
	)

	g.rightContent = container.NewStack(container.NewVScroll(g.infoBox))
}

func (g *gui) rightColumn() fyne.CanvasObject {
	infoBtn := widget.NewButton("对方资料", func() { g.switchRight(false) })
	taskBtn := widget.NewButton("文件传输", func() { g.switchRight(true) })
	width := canvas.NewRectangle(color.Transparent)
	width.SetMinSize(fyne.NewSize(290, 1))
	return container.NewBorder(
		container.NewVBox(width, container.NewGridWithColumns(2, infoBtn, taskBtn), widget.NewSeparator()),
		nil, nil, nil, g.rightContent,
	)
}

// switchRight 切换右栏视图（false=资料 true=传输）
func (g *gui) switchRight(tasks bool) {
	g.showTasks = tasks
	if tasks {
		g.rightContent.Objects = []fyne.CanvasObject{g.taskPanel}
	} else {
		g.rightContent.Objects = []fyne.CanvasObject{container.NewVScroll(g.infoBox)}
	}
	g.rightContent.Refresh()
}

// ── 主题切换 ────────────────────────────────────────────────

func (g *gui) toggleTheme() {
	g.set.Dark = !g.set.Dark
	g.applyTheme()
	_ = g.set.save(g.dataDir)
}

func (g *gui) applyTheme() {
	g.fa.Settings().SetTheme(macTheme{dark: g.set.Dark})
	g.chatBg.FillColor = chatBgColor(g.set.Dark)
	g.chatBg.Refresh()
	g.openConvByKey(g.curKey) // 重绘气泡配色
}

// ── 会话切换与气泡渲染 ──────────────────────────────────────

func (g *gui) openConv(key, title string) {
	g.mu.Lock()
	g.curKey, g.curTitle = key, title
	msgs := append([]msgItem(nil), g.convs[key]...)
	g.mu.Unlock()

	g.chatTitle.SetText(title)
	g.msgBox.Objects = nil
	for _, m := range msgs {
		g.msgBox.Add(bubble(m, g.set.Dark))
	}
	g.msgBox.Refresh()
	g.msgScroll.ScrollToBottom()
	g.updateInfo()
}

// appendMsg 追加消息到会话，如为当前会话则实时渲染
func (g *gui) appendMsg(key string, m msgItem) {
	g.mu.Lock()
	g.convs[key] = append(g.convs[key], m)
	if n := len(g.convs[key]); n > 500 {
		g.convs[key] = g.convs[key][n-500:]
	}
	cur := g.curKey == key
	g.mu.Unlock()

	fyne.Do(func() {
		if cur {
			g.msgBox.Add(bubble(m, g.set.Dark))
			g.msgBox.Refresh()
			g.msgScroll.ScrollToBottom()
		} else {
			g.openConvByKey(key)
		}
	})
}

// openConvByKey 根据 key 推导标题并打开会话
func (g *gui) openConvByKey(key string) {
	title := key
	switch {
	case key == broadcastKey:
		title = "广播"
	case strings.HasPrefix(key, groupPrefix):
		title = "群:" + key[len(groupPrefix):]
	default:
		title = g.peerName(key)
	}
	g.openConv(key, title)
}

// bubble 渲染一条聊天气泡
func bubble(m msgItem, dark bool) fyne.CanvasObject {
	meta := canvas.NewText(fmt.Sprintf("%s  %s", m.sender, ts(m.ts)), metaColor(dark))
	meta.TextSize = 11

	lbl := widget.NewLabel(softWrap(m.text, 52))
	bg := canvas.NewRectangle(bubbleColor(dark, m.mine))
	bg.CornerRadius = 10
	body := container.NewStack(bg, lbl)

	col := container.NewVBox(meta, body)
	if m.mine {
		return container.NewHBox(layout.NewSpacer(), col)
	}
	return container.NewHBox(col, layout.NewSpacer())
}

// softWrap 手动软换行：half-width 计 1、CJK 计 2，超宽插入换行
func softWrap(s string, width int) string {
	var b strings.Builder
	for i, line := range strings.Split(s, "\n") {
		if i > 0 {
			b.WriteByte('\n')
		}
		n := 0
		for _, r := range line {
			b.WriteRune(r)
			if r > 0x7F {
				n += 2
			} else {
				n++
			}
			if n >= width {
				b.WriteByte('\n')
				n = 0
			}
		}
	}
	return b.String()
}

// ── 右栏：对方资料 ──────────────────────────────────────────

func (g *gui) updateInfo() {
	g.mu.Lock()
	key := g.curKey
	var peer *discovery.Peer
	for i := range g.peers {
		if g.peers[i].ID == key {
			peer = &g.peers[i]
			break
		}
	}
	g.mu.Unlock()

	g.infoBox.Objects = nil
	title := widget.NewLabelWithStyle("对方资料", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	g.infoBox.Add(title)
	g.infoBox.Add(widget.NewSeparator())

	addRow := func(k, v string) {
		lbl := widget.NewLabel(v)
		lbl.Wrapping = fyne.TextWrapBreak
		g.infoBox.Add(container.NewVBox(
			widget.NewLabelWithStyle(k, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}), lbl,
		))
	}

	switch {
	case key == broadcastKey:
		addRow("会话", "广播频道")
		addRow("说明", "消息将发送给局域网内所有在线用户")
	case strings.HasPrefix(key, groupPrefix):
		name := key[len(groupPrefix):]
		addRow("群名", name)
		members := g.app.Chat.Groups()[name]
		addRow("成员数", strconv.Itoa(len(members)))
	case peer != nil:
		status := "离线"
		if peer.Online {
			status = "在线"
		}
		if r := g.set.Remarks[peer.ID]; r != "" {
			addRow("备注", r)
		}
		addRow("昵称", peer.Name)
		addRow("状态", status)
		if !g.set.HideIP {
			addRow("IP 地址", peer.IP.String())
			addRow("UDP 端口", strconv.Itoa(peer.UDPPort))
			addRow("TCP 端口", strconv.Itoa(peer.TCPPort))
		}
		addRow("节点 ID", peer.ID)
		addRow("公钥指纹", identity.Fingerprint(peer.PubKey))
		addRow("最后活跃", peer.LastSeen.Format("15:04:05"))
		pid := peer.ID
		remarkBtn := widget.NewButtonWithIcon("设置备注", theme.DocumentCreateIcon(), func() {
			g.editRemark(pid)
		})
		g.infoBox.Add(remarkBtn)
	default:
		addRow("提示", "在左侧选择一个用户查看资料")
	}
	g.infoBox.Refresh()
	if !g.showTasks {
		g.switchRight(false)
	}
}

// editRemark 修改指定节点的备注名
func (g *gui) editRemark(peerID string) {
	e := widget.NewEntry()
	e.SetText(g.set.Remarks[peerID])
	e.SetPlaceHolder("留空则清除备注")
	d := dialog.NewForm("设置备注", "保存", "取消",
		[]*widget.FormItem{widget.NewFormItem("备注名", e)},
		func(ok bool) {
			if !ok {
				return
			}
			r := strings.TrimSpace(e.Text)
			if r == "" {
				delete(g.set.Remarks, peerID)
			} else {
				g.set.Remarks[peerID] = r
			}
			_ = g.set.save(g.dataDir)
			g.rebuildLeftRows()
			g.peersList.Refresh()
			g.updateInfo()
			g.note("备注已保存")
		}, g.win)
	d.Resize(fyne.NewSize(420, 180))
	d.Show()
}

// ── 设置面板 ────────────────────────────────────────────────

func (g *gui) showSettings() {
	s := g.set

	// 头像
	avatarImg := canvas.NewImageFromResource(appIcon)
	if s.Avatar != "" {
		avatarImg = canvas.NewImageFromFile(s.Avatar)
	}
	avatarImg.FillMode = canvas.ImageFillContain
	avatarImg.SetMinSize(fyne.NewSize(64, 64))
	newAvatar := s.Avatar
	pickAvatar := widget.NewButton("选择头像…", func() {
		fd := dialog.NewFileOpen(func(rc fyne.URIReadCloser, err error) {
			if err != nil || rc == nil {
				return
			}
			defer rc.Close()
			dst := filepath.Join(g.dataDir, "avatar"+strings.ToLower(filepath.Ext(rc.URI().Name())))
			if err := copyFile(rc, dst); err != nil {
				g.note("头像保存失败: " + err.Error())
				return
			}
			newAvatar = dst
			avatarImg.File = dst
			avatarImg.Resource = nil
			avatarImg.Refresh()
		}, g.win)
		fd.SetFilter(storage.NewExtensionFileFilter([]string{".png", ".jpg", ".jpeg", ".gif", ".bmp"}))
		fd.Show()
		fd.Resize(fyne.NewSize(760, 520))
	})

	nickE := widget.NewEntry()
	nickE.SetText(g.app.ID.Name())

	subnetE := widget.NewEntry()
	subnetE.SetText(s.Subnet)
	subnetE.SetPlaceHolder("如 192.168.1.0/24，留空不过滤")

	dlE := widget.NewEntry()
	dlE.SetText(g.app.Transfer.Downloads())
	dlBrowse := widget.NewButton("浏览…", func() {
		fd := dialog.NewFolderOpen(func(dir fyne.ListableURI, err error) {
			if err != nil || dir == nil {
				return
			}
			dlE.SetText(dir.Path())
		}, g.win)
		fd.Show()
		fd.Resize(fyne.NewSize(760, 520))
	})

	autoC := widget.NewCheck("开机自动启动", nil)
	autoC.SetChecked(s.AutoStart)
	hideC := widget.NewCheck("界面隐藏 IP 地址", nil)
	hideC.SetChecked(s.HideIP)
	soundC := widget.NewCheck("新消息提示音", nil)
	soundC.SetChecked(s.Sound)
	darkC := widget.NewCheck("暗黑主题", nil)
	darkC.SetChecked(s.Dark)
	openC := widget.NewCheck("接收完成后自动打开文件", nil)
	openC.SetChecked(s.AutoOpen)

	notifyR := widget.NewRadioGroup([]string{"任务栏闪烁", "自动弹出窗口"}, nil)
	if s.NotifyPopup {
		notifyR.SetSelected("自动弹出窗口")
	} else {
		notifyR.SetSelected("任务栏闪烁")
	}

	closeR := widget.NewRadioGroup([]string{"最小化到托盘", "退出程序"}, nil)
	if s.CloseToTray {
		closeR.SetSelected("最小化到托盘")
	} else {
		closeR.SetSelected("退出程序")
	}

	form := container.NewVBox(
		container.NewHBox(avatarImg, pickAvatar),
		widget.NewForm(
			widget.NewFormItem("昵称", nickE),
			widget.NewFormItem("局域网段", subnetE),
			widget.NewFormItem("接收目录", container.NewBorder(nil, nil, nil, dlBrowse, dlE)),
		),
		widget.NewSeparator(),
		autoC, hideC, soundC, darkC, openC,
		widget.NewLabelWithStyle("新消息提醒方式", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		notifyR,
		widget.NewLabelWithStyle("点击关闭按钮时", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		closeR,
	)

	d := dialog.NewCustomConfirm("设置", "保存", "取消", container.NewVScroll(form),
		func(ok bool) {
			if !ok {
				return
			}
			// 昵称
			if nick := strings.TrimSpace(nickE.Text); nick != "" && nick != g.app.ID.Name() {
				g.app.ID.SetName(nick)
				s.Nickname = nick
			}
			// 网段
			subnet := strings.TrimSpace(subnetE.Text)
			if subnet != "" {
				if _, _, err := net.ParseCIDR(subnet); err != nil {
					g.note("网段格式无效（应为 CIDR，如 192.168.1.0/24），未保存该项")
					subnet = s.Subnet
				}
			}
			s.Subnet = subnet
			// 下载目录
			if dl := strings.TrimSpace(dlE.Text); dl != "" && dl != g.app.Transfer.Downloads() {
				if err := g.app.Transfer.SetDownloads(dl); err != nil {
					g.note("下载目录设置失败: " + err.Error())
				} else {
					s.Downloads = dl
				}
			}
			// 开机自启
			if autoC.Checked != s.AutoStart {
				if err := setAutoStart(autoC.Checked); err != nil {
					g.note("开机自启设置失败: " + err.Error())
				} else {
					s.AutoStart = autoC.Checked
				}
			}
		s.HideIP = hideC.Checked
		s.Sound = soundC.Checked
		s.AutoOpen = openC.Checked
		s.NotifyPopup = notifyR.Selected == "自动弹出窗口"
		s.CloseToTray = closeR.Selected == "最小化到托盘"
			s.Avatar = newAvatar
			if darkC.Checked != s.Dark {
				s.Dark = darkC.Checked
				g.applyTheme()
			}
			_ = s.save(g.dataDir)
			g.refreshMe()
			g.refreshPeersNow()
			g.updateInfo()
			g.note("设置已保存")
		}, g.win)
	d.Resize(fyne.NewSize(560, 660))
	d.Show()
}

// copyFile 将 reader 内容写入 dst
func copyFile(r io.Reader, dst string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

// ── 事件与定时刷新 ──────────────────────────────────────────

func (g *gui) start() {
	ch, _ := g.app.Bus.Subscribe(512)
	go func() {
		for ev := range ch {
			switch ev.Topic {
			case bus.TopicPeerOnline:
				if p, ok := ev.Data.(discovery.PeerEvent); ok {
					g.note("上线: " + p.Name)
				}
			case bus.TopicPeerOffline:
				if p, ok := ev.Data.(discovery.PeerEvent); ok {
					g.note("下线: " + p.Name)
				}
			case bus.TopicPeerWarn:
				if p, ok := ev.Data.(discovery.PeerEvent); ok {
					g.note("安全告警: " + p.Name + " " + p.Addr)
				}
			case bus.TopicMsgRecv:
				if m, ok := ev.Data.(chat.Message); ok {
					g.appendMsg(msgKey(m), msgItem{sender: m.FromName, text: m.Text, ts: m.TS})
					g.notifyIncoming()
				}
			case bus.TopicMsgSent:
				if m, ok := ev.Data.(chat.Message); ok {
					g.appendMsg(msgKey(m), msgItem{sender: "我", text: m.Text, ts: m.TS, mine: true})
				}
			case bus.TopicMsgQueued:
				g.note("对方离线，消息已存入离线队列")
			case bus.TopicTransferOffer:
				if o, ok := ev.Data.(transfer.OfferEvent); ok {
					g.note(fmt.Sprintf("收到文件请求: %s 发来 %d 个文件", o.PeerName, o.Files))
					g.notifyIncoming()
					fyne.Do(func() { g.switchRight(true) })
				}
		case bus.TopicTransferDone:
			if r, ok := ev.Data.(transfer.ResultEvent); ok {
				g.note("传输完成: " + r.Message)
				if g.set.AutoOpen {
					if paths, err := g.app.Transfer.SavedPaths(r.TaskID); err == nil {
						for _, p := range paths {
							_ = openPath(p)
						}
					}
				}
			}
			case bus.TopicTransferPaused:
				if r, ok := ev.Data.(transfer.ResultEvent); ok {
					g.note("传输暂停: " + r.Message)
				}
			case bus.TopicTransferError:
				if r, ok := ev.Data.(transfer.ResultEvent); ok {
					g.note("传输异常: " + r.Message)
				}
			}
		}
	}()

	go func() {
		tk := time.NewTicker(800 * time.Millisecond)
		for range tk.C {
			g.refreshPeers()
			g.refreshTasks()
		}
	}()
}

// notifyIncoming 新消息提醒：提示音 + 弹出/闪烁
func (g *gui) notifyIncoming() {
	if g.set.Sound {
		playBeep()
	}
	if g.set.NotifyPopup {
		fyne.Do(func() {
			g.win.Show()
			g.win.RequestFocus()
		})
	} else {
		flashTaskbar(winTitle)
	}
}

// display 备注优先的显示名
func (g *gui) display(p discovery.Peer) string {
	if r := g.set.Remarks[p.ID]; r != "" {
		return r
	}
	return p.Name
}

func (g *gui) rebuildLeftRows() {
	groups := g.app.Chat.Groups()
	names := make([]string, 0, len(groups))
	for n := range groups {
		names = append(names, n)
	}
	sort.Strings(names)

	g.mu.Lock()
	rows := []leftRow{{key: broadcastKey, title: "📢 广播", sub: "所有在线用户"}}
	for _, n := range names {
		rows = append(rows, leftRow{
			key: groupPrefix + n, title: "👥 " + n,
			sub: fmt.Sprintf("%d 名成员", len(groups[n])),
		})
	}
	for _, p := range g.peers {
		sub := p.IP.String()
		if g.set.HideIP {
			sub = "在线设备"
		}
		rows = append(rows, leftRow{
			key: p.ID, title: g.display(p), sub: sub,
			online: p.Online, isPeer: true,
		})
	}
	g.leftRows = rows
	g.mu.Unlock()
}

// filterPeers 按设置的网段过滤节点
func (g *gui) filterPeers(peers []discovery.Peer) []discovery.Peer {
	subnet := strings.TrimSpace(g.set.Subnet)
	if subnet == "" {
		return peers
	}
	_, ipnet, err := net.ParseCIDR(subnet)
	if err != nil {
		return peers
	}
	out := peers[:0]
	for _, p := range peers {
		if ipnet.Contains(p.IP) {
			out = append(out, p)
		}
	}
	return out
}

func (g *gui) refreshPeers() {
	peers := g.filterPeers(g.app.Disc.Peers())
	g.mu.Lock()
	g.peers = peers
	g.mu.Unlock()
	g.rebuildLeftRows()
	fyne.Do(func() {
		g.peersList.Refresh()
		g.updateInfo()
	})
}

// refreshPeersNow 主线程内的立即刷新（设置保存后调用）
func (g *gui) refreshPeersNow() {
	peers := g.filterPeers(g.app.Disc.Peers())
	g.mu.Lock()
	g.peers = peers
	g.mu.Unlock()
	g.rebuildLeftRows()
	g.peersList.Refresh()
}

func (g *gui) refreshTasks() {
	tasks := g.app.Transfer.Tasks()
	active := 0
	for _, t := range tasks {
		switch t.State {
		case transfer.StateOffering, transfer.StatePendingLocal,
			transfer.StateTransferring, transfer.StatePaused:
			active++
		}
	}
	prev := g.prevActive
	g.prevActive = active

	fyne.Do(func() {
		g.taskBox.Objects = nil
		for _, t := range tasks {
			g.taskBox.Add(g.taskRow(t))
		}
		g.taskBox.Refresh()
		// 自动切换：出现活动任务 → 传输面板；全部结束 → 资料面板
		if prev == 0 && active > 0 && !g.showTasks {
			g.switchRight(true)
		} else if prev > 0 && active == 0 && g.showTasks {
			g.switchRight(false)
		}
	})
}

// ── 发送逻辑 ────────────────────────────────────────────────

func (g *gui) sendCurrent() {
	g.mu.Lock()
	key := g.curKey
	g.mu.Unlock()
	text := strings.TrimSpace(g.entry.Text)
	if key == "" || text == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	switch {
	case key == broadcastKey:
		if err := g.app.Chat.Broadcast(text); err != nil {
			g.note("广播失败: " + err.Error())
		} else {
			g.appendMsg(broadcastKey, msgItem{sender: "我(广播)", text: text, ts: time.Now().UnixMilli(), mine: true})
		}
	case strings.HasPrefix(key, groupPrefix):
		if err := g.app.Chat.SendGroup(ctx, key[len(groupPrefix):], text); err != nil {
			g.note("群发失败: " + err.Error())
		}
	default:
		if err := g.app.Chat.SendText(ctx, key, text); err != nil {
			if errors.Is(err, chat.ErrPeerOffline) {
				g.note("对方离线，消息已进入离线队列")
			} else {
				g.note("发送失败: " + err.Error())
			}
		}
	}
	g.entry.SetText("")
}

// ── 文件发送：按钮选择 + 拖拽 ───────────────────────────────

// sendPaths 向当前会话对应的用户发送一批文件/文件夹
func (g *gui) sendPaths(paths []string) {
	g.mu.Lock()
	key := g.curKey
	g.mu.Unlock()
	if key == "" || key == broadcastKey || strings.HasPrefix(key, groupPrefix) {
		dialog.ShowInformation("提示", "请先在左侧选择一个用户，再发送文件", g.win)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		id, err := g.app.Transfer.SendPaths(ctx, key, paths)
		if err != nil {
			g.note("发送失败: " + err.Error())
			return
		}
		g.note("已发出传输请求: " + shortID(id))
		fyne.Do(func() { g.switchRight(true) })
	}()
}

// onDropped 窗口拖拽文件回调
func (g *gui) onDropped(_ fyne.Position, uris []fyne.URI) {
	if len(uris) == 0 {
		return
	}
	paths := make([]string, 0, len(uris))
	for _, u := range uris {
		if p := u.Path(); p != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) > 0 {
		g.sendPaths(paths)
	}
}

func (g *gui) pickAndSend(dirOnly bool) {
	defer g.recoverUI("文件选择")
	if dirOnly {
		fd := dialog.NewFolderOpen(func(dir fyne.ListableURI, err error) {
			if err != nil || dir == nil {
				return
			}
			g.sendPaths([]string{dir.Path()})
		}, g.win)
		fd.Show()
		fd.Resize(fyne.NewSize(780, 540))
	} else {
		// 使用 Windows 原生文件对话框（ms 级响应，无 CMD 窗口）
		path, err := nativeFileOpen("选择要发送的文件")
		if err == nil && path != "" {
			g.sendPaths([]string{path})
		}
	}
}

// ── 传输任务面板 ────────────────────────────────────────────

func (g *gui) taskRow(t transfer.TaskView) fyne.CanvasObject {
	prefix := shortID(t.ID)
	pct := float64(0)
	if t.Total > 0 {
		pct = float64(t.Done) / float64(t.Total)
	}
	bar := widget.NewProgressBar()
	bar.SetValue(pct)
	dir := "发送 →"
	if t.Dir == transfer.DirRecv {
		dir = "接收 ←"
	}
	title := widget.NewLabel(fmt.Sprintf("#%s %s %s", prefix, dir, t.PeerName))
	title.TextStyle = fyne.TextStyle{Bold: true}
	info := widget.NewLabel(fmt.Sprintf("%s  %s / %s  %s/s",
		stateText(t.State), human(t.Done), human(t.Total), human(int64(t.SpeedBps))))
	row := container.NewVBox(title, info, bar)

	btnBox := container.NewHBox()
	switch {
	case t.Dir == transfer.DirRecv && t.State == transfer.StatePendingLocal:
		btnBox.Add(widget.NewButton("接受", func() {
			g.act(func(c context.Context) error { return g.app.Transfer.Accept(c, prefix) })
		}))
		btnBox.Add(widget.NewButton("拒绝", func() {
			g.act(func(c context.Context) error { return g.app.Transfer.Reject(c, prefix, "") })
		}))
	case t.State == transfer.StateTransferring:
		btnBox.Add(widget.NewButton("暂停", func() {
			_ = g.app.Transfer.Pause(prefix)
		}))
	case t.State == transfer.StatePaused:
		btnBox.Add(widget.NewButton("继续", func() {
			g.act(func(c context.Context) error { return g.app.Transfer.Resume(c, prefix) })
		}))
	case t.Dir == transfer.DirRecv && t.State == transfer.StateCompleted:
		btnBox.Add(widget.NewButton("打开", func() {
			paths, err := g.app.Transfer.SavedPaths(prefix)
			if err != nil || len(paths) == 0 {
				g.note("找不到已接收的文件")
				return
			}
			for _, p := range paths {
				if err := openPath(p); err != nil {
					g.note("打开失败：" + err.Error())
				}
			}
		}))
		btnBox.Add(widget.NewButton("打开目录", func() {
			if err := openPath(g.app.Transfer.Downloads()); err != nil {
				g.note("打开目录失败：" + err.Error())
			}
		}))
	}
	if len(btnBox.Objects) > 0 {
		row.Add(btnBox)
	}
	return container.NewVBox(row, widget.NewSeparator())
}

func stateText(s transfer.State) string {
	switch s {
	case transfer.StateOffering:
		return "等待对方接受"
	case transfer.StatePendingLocal:
		return "等待接受"
	case transfer.StateTransferring:
		return "传输中"
	case transfer.StatePaused:
		return "已暂停"
	case transfer.StateCompleted:
		return "已完成"
	case transfer.StateFailed:
		return "失败"
	case transfer.StateRejected:
		return "已拒绝"
	}
	return string(s)
}

func (g *gui) applyRate() {
	v := strings.TrimSpace(g.rateEntry.Text)
	kbps := 0
	if v != "" {
		kbps, _ = strconv.Atoi(v)
	}
	if kbps < 0 {
		kbps = 0
	}
	g.app.Transfer.SetRateKBps(kbps)
	if kbps == 0 {
		g.note("已取消限速")
	} else {
		g.note(fmt.Sprintf("发送限速已设为 %d KB/s", kbps))
	}
}

// ── 群组 ────────────────────────────────────────────────────

func (g *gui) newGroup() {
	nameE := widget.NewEntry()
	nameE.SetPlaceHolder("输入群名称")

	// 用复选框直观选择成员，替代序号输入
	g.mu.Lock()
	peers := append([]discovery.Peer(nil), g.peers...)
	g.mu.Unlock()
	checks := make([]*widget.Check, len(peers))
	memberBox := container.NewVBox()
	for i, p := range peers {
		label := g.display(p)
		if !g.set.HideIP {
			label += "  (" + p.IP.String() + ")"
		}
		checks[i] = widget.NewCheck(label, nil)
		memberBox.Add(checks[i])
	}
	if len(peers) == 0 {
		memberBox.Add(widget.NewLabel("暂无在线用户"))
	}

	content := container.NewVBox(
		widget.NewForm(widget.NewFormItem("群名", nameE)),
		widget.NewLabelWithStyle("选择成员", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		container.NewVScroll(memberBox),
	)

	d := dialog.NewCustomConfirm("新建群", "创建", "取消", content,
		func(ok bool) {
			if !ok {
				return
			}
			name := strings.TrimSpace(nameE.Text)
			if name == "" {
				g.note("群名不能为空")
				return
			}
			var members []string
			for i, c := range checks {
				if c != nil && c.Checked {
					members = append(members, peers[i].ID)
				}
			}
			members = append(members, g.app.ID.ID)
			if err := g.app.Chat.CreateGroup(name, members); err != nil {
				g.note("建群失败: " + err.Error())
				return
			}
			g.rebuildLeftRows()
			g.peersList.Refresh()
			g.openConv(groupPrefix+name, "群:"+name)
			g.note("群已创建: " + name)
		}, g.win)
	d.Resize(fyne.NewSize(480, 460))
	d.Show()
}

// ── 辅助 ────────────────────────────────────────────────────

func (g *gui) act(fn func(context.Context) error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := fn(ctx); err != nil {
		g.note("操作失败: " + err.Error())
	}
}

// note 底部状态栏提示
func (g *gui) note(msg string) {
	line := time.Now().Format("15:04:05") + "  " + msg
	fyne.Do(func() { g.statusLabel.SetText(line) })
}

func (g *gui) peerName(id string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range g.peers {
		if p.ID == id {
			if r := g.set.Remarks[id]; r != "" {
				return r
			}
			return p.Name
		}
	}
	return id
}

func msgKey(m chat.Message) string {
	if m.Bcast {
		return broadcastKey
	}
	if m.Group != "" {
		return groupPrefix + m.Group
	}
	if m.Outgoing {
		return m.To
	}
	return m.From
}

func ts(ms int64) string { return time.UnixMilli(ms).Format("15:04:05") }

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// openPath 在 Windows 上用关联程序打开文件或目录（无控制台窗口闪烁）。
func openPath(path string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", path).Start()
}

func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}
