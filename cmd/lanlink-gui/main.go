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
	unread int
}

// peerPalette 在线用户头像的彩色调色板（按节点 ID 哈希取色，稳定不变）
var peerPalette = []color.NRGBA{
	{0x2E, 0x86, 0xDE, 0xFF}, // 蓝
	{0xE7, 0x4C, 0x3C, 0xFF}, // 红
	{0x27, 0xAE, 0x60, 0xFF}, // 绿
	{0x8E, 0x44, 0xAD, 0xFF}, // 紫
	{0xE6, 0x7E, 0x22, 0xFF}, // 橙
	{0x16, 0xA0, 0x85, 0xFF}, // 青
	{0xD9, 0x53, 0x8C, 0xFF}, // 粉
	{0x2C, 0x82, 0xC9, 0xFF}, // 靛
}

var grayAvatar = color.NRGBA{0x9E, 0x9E, 0x9E, 0xFF}

func peerColor(id string) color.NRGBA {
	h := 0
	for _, c := range id {
		h = h*31 + int(c)
	}
	if h < 0 {
		h = -h
	}
	return peerPalette[h%len(peerPalette)]
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
	unread   map[string]int // 各会话未读条数
	blinkOn  bool           // 未读红点闪烁开关（定时器翻转）
	historyFile string      // 历史消息文件路径
	histDirty   bool        // 内存会话有变更，需落盘

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
		convs: make(map[string][]msgItem), unread: make(map[string]int)}
	g.historyFile = filepath.Join(*dataDir, historyFileName)
	g.loadHistory() // 启动即恢复历史消息
	g.build()
	g.setupTray()
	w.Resize(fyne.NewSize(1180, 760))
	w.SetOnDropped(g.onDropped)
	w.SetCloseIntercept(func() {
		g.saveHistory() // 退出/最小化前确保历史落盘
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
			// 头像：圆形底 + 首字母；在线彩色 / 离线灰色
			circle := canvas.NewCircle(grayAvatar)
			initial := canvas.NewText("?", color.White)
			initial.TextStyle = fyne.TextStyle{Bold: true}
			initial.TextSize = 15
			sizer := canvas.NewRectangle(color.Transparent)
			sizer.SetMinSize(fyne.NewSize(38, 38))
			avatar := container.NewStack(sizer, circle, container.NewCenter(initial))

			title := canvas.NewText("模板", theme.Color(theme.ColorNameForeground))
			title.TextStyle = fyne.TextStyle{Bold: true}
			title.TextSize = theme.TextSize()
			sub := canvas.NewText("模板", grayAvatar)
			sub.TextSize = theme.TextSize() - 3
			info := container.NewVBox(title, sub)

			return container.NewBorder(nil, nil, avatar, nil, container.NewPadded(info))
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			g.mu.Lock()
			if id < 0 || id >= len(g.leftRows) {
				g.mu.Unlock()
				return
			}
			r := g.leftRows[id]
			blink := g.blinkOn
			g.mu.Unlock()

			border := o.(*fyne.Container)
			var avatar, center *fyne.Container
			for _, obj := range border.Objects {
				if c, ok := obj.(*fyne.Container); ok {
					if _, isStack := c.Objects[0].(*canvas.Rectangle); isStack && len(c.Objects) == 3 {
						avatar = c
					} else {
						center = c
					}
				}
			}
			if avatar == nil || center == nil {
				return
			}
			circle := avatar.Objects[1].(*canvas.Circle)
			initial := avatar.Objects[2].(*fyne.Container).Objects[0].(*canvas.Text)
			info := center.Objects[0].(*fyne.Container)
			title := info.Objects[0].(*canvas.Text)
			sub := info.Objects[1].(*canvas.Text)

			// 头像颜色与首字符
			name := r.title
			switch {
			case r.key == broadcastKey:
				circle.FillColor = color.NRGBA{0xF3, 0x9C, 0x12, 0xFF}
				initial.Text = "广"
			case strings.HasPrefix(r.key, groupPrefix):
				circle.FillColor = color.NRGBA{0x34, 0x98, 0xDB, 0xFF}
				initial.Text = "群"
			case r.isPeer && r.online:
				circle.FillColor = peerColor(r.key)
				initial.Text = firstRune(name)
			default: // 离线用户
				circle.FillColor = grayAvatar
				initial.Text = firstRune(name)
			}

			// 昵称颜色：在线/频道用前景色（彩色头像已区分），离线灰色
			if r.isPeer && !r.online {
				title.Color = grayAvatar
			} else {
				title.Color = theme.Color(theme.ColorNameForeground)
			}

			// 未读闪烁红点
			prefix := ""
			if r.unread > 0 {
				if blink {
					prefix = fmt.Sprintf("🔴%d ", r.unread)
				} else {
					prefix = fmt.Sprintf("⭕%d ", r.unread)
				}
			}
			suffix := ""
			if r.isPeer && !r.online {
				suffix = "（离线）"
			}
			title.Text = prefix + name + suffix
			sub.Text = r.sub
			circle.Refresh()
			initial.Refresh()
			title.Refresh()
			sub.Refresh()
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

	refreshBtn := widget.NewButtonWithIcon("刷新", theme.ViewRefreshIcon(), func() {
		g.app.Disc.Refresh()
	})
	settingsBtn := widget.NewButtonWithIcon("设置", theme.SettingsIcon(), g.showSettings)
	themeBtn := widget.NewButtonWithIcon("主题", theme.ColorPaletteIcon(), g.toggleTheme)
	newGroup := widget.NewButtonWithIcon("建群", theme.ContentAddIcon(), g.newGroup)
	tools := container.NewGridWithColumns(4, refreshBtn, settingsBtn, themeBtn, newGroup)

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

	histBtn := widget.NewButtonWithIcon("历史记录", theme.FileTextIcon(), g.viewHistory)
	titleRow := container.NewBorder(nil, nil, nil, histBtn, g.chatTitle)
	top := container.NewVBox(titleRow, widget.NewSeparator())
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
	delete(g.unread, key) // 打开会话即清零未读
	g.mu.Unlock()
	g.rebuildLeftRows()
	g.peersList.Refresh()

	g.chatTitle.SetText(title)
	g.msgBox.Objects = nil
	for _, m := range msgs {
		g.msgBox.Add(bubble(m, g.set.Dark))
	}
	g.msgBox.Refresh()
	g.msgScroll.ScrollToBottom()
	g.updateInfo()
}

// appendMsg 追加消息到会话，如为当前会话则实时渲染；
// 非当前会话收到对方消息时累计未读（由闪烁定时器提示），不再强制切换会话。
func (g *gui) appendMsg(key string, m msgItem) {
	g.mu.Lock()
	g.convs[key] = append(g.convs[key], m)
	if n := len(g.convs[key]); n > 500 {
		g.convs[key] = g.convs[key][n-500:]
	}
	cur := g.curKey == key
	if !cur && !m.mine {
		if g.unread == nil {
			g.unread = make(map[string]int)
		}
		g.unread[key]++
	}
	g.histDirty = true // 内存会话已变更，标记落盘
	g.mu.Unlock()

	fyne.Do(func() {
		if cur {
			g.msgBox.Add(bubble(m, g.set.Dark))
			g.msgBox.Refresh()
			g.msgScroll.ScrollToBottom()
		} else if m.mine {
			// 自己发出的消息（如群发回显）切到对应会话
			g.openConvByKey(key)
		} else {
			// 对方消息：仅刷新左栏未读红点，不打断当前会话
			g.rebuildLeftRows()
			g.peersList.Refresh()
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
	lbl.Selectable = true // 允许在聊天气泡中选择并复制文本
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
		delBtn := widget.NewButtonWithIcon("删除此设备", theme.DeleteIcon(), func() {
			g.app.RemovePeer(pid)
			g.mu.Lock()
			delete(g.convs, pid)
			if g.curKey == pid {
				g.curKey = broadcastKey
				g.curTitle = "广播"
			}
			g.mu.Unlock()
			g.refreshPeersNow()
			g.openConvByKey(g.curKey)
		})
		delBtn.Importance = widget.DangerImportance
		g.infoBox.Add(delBtn)
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
			// 未读红点闪烁：有未读时每个周期翻转显隐
			g.mu.Lock()
			has := false
			for _, n := range g.unread {
				if n > 0 {
					has = true
					break
				}
			}
			if has {
				g.blinkOn = !g.blinkOn
			} else {
				g.blinkOn = false
			}
			dirty := g.histDirty
			g.histDirty = false
			g.mu.Unlock()

			// 会话有变更则落盘（退出前兜底，避免丢失历史）
			if dirty {
				g.saveHistory()
			}
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
	rows := []leftRow{{key: broadcastKey, title: "广播", sub: "所有在线用户", unread: g.unread[broadcastKey]}}
	for _, n := range names {
		rows = append(rows, leftRow{
			key: groupPrefix + n, title: n,
			sub:    fmt.Sprintf("%d 名成员", len(groups[n])),
			unread: g.unread[groupPrefix+n],
		})
	}
	for _, p := range g.peers {
		sub := p.IP.String()
		if g.set.HideIP {
			sub = "在线设备"
		}
		if !p.Online {
			sub = "离线 · 可发离线消息"
		}
		rows = append(rows, leftRow{
			key: p.ID, title: g.display(p), sub: sub,
			online: p.Online, isPeer: true, unread: g.unread[p.ID],
		})
	}
	g.leftRows = rows
	g.mu.Unlock()
}

// firstRune 取字符串首字符（用于头像展示）
func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return "?"
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
				g.note("对方离线，消息已进入离线队列，上线后自动送达")
				// 本地回显离线暂存消息，让用户看到已发出的内容
				g.appendMsg(key, msgItem{sender: "我", text: "[离线暂存] " + text, ts: time.Now().UnixMilli(), mine: true})
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

// convTitle 根据会话 key 推导显示标题（历史浏览等场景使用）
func (g *gui) convTitle(key string) string {
	switch {
	case key == broadcastKey:
		return "广播"
	case strings.HasPrefix(key, groupPrefix):
		return "群:" + key[len(groupPrefix):]
	default:
		return g.peerName(key)
	}
}

// truncate 截断字符串到 n 个 rune（用于历史列表预览）
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// viewHistory 弹出历史消息浏览窗口：左侧会话列表，右侧完整消息气泡
func (g *gui) viewHistory() {
	g.mu.Lock()
	keys := make([]string, 0, len(g.convs))
	for k := range g.convs {
		keys = append(keys, k)
	}
	g.mu.Unlock()
	sort.Strings(keys)

	msgView := container.NewVBox()
	scroll := container.NewVScroll(msgView)
	scroll.SetMinSize(fyne.NewSize(540, 440))

	list := widget.NewList(
		func() int { return len(keys) },
		func() fyne.CanvasObject {
			title := widget.NewLabelWithStyle("会话", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
			prev := widget.NewLabel("")
			prev.Wrapping = fyne.TextWrapWord
			return container.NewVBox(title, prev)
		},
		func(id widget.ListItemID, o fyne.CanvasObject) {
			k := keys[id]
			box := o.(*fyne.Container)
			title := box.Objects[0].(*widget.Label)
			prev := box.Objects[1].(*widget.Label)
			g.mu.Lock()
			msgs := g.convs[k]
			g.mu.Unlock()
			title.SetText(g.convTitle(k))
			if len(msgs) > 0 {
				last := msgs[len(msgs)-1]
				prev.SetText(fmt.Sprintf("[%s] %s", ts(last.ts), truncate(last.text, 28)))
			} else {
				prev.SetText("（无消息）")
			}
			title.Refresh()
			prev.Refresh()
		},
	)
	list.OnSelected = func(id widget.ListItemID) {
		k := keys[id]
		g.mu.Lock()
		msgs := append([]msgItem(nil), g.convs[k]...)
		g.mu.Unlock()
		msgView.Objects = nil
		for _, m := range msgs {
			msgView.Add(bubble(m, g.set.Dark))
		}
		msgView.Refresh()
		scroll.ScrollToTop()
	}

	content := container.NewHSplit(container.NewVScroll(list), scroll)
	content.SetOffset(0.32)
	d := dialog.NewCustom("历史消息", "关闭", content, g.win)
	d.Resize(fyne.NewSize(920, 540))
	d.Show()
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
