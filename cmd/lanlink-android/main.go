package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"image/color"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	fyneApp "fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"lanlink/internal/app"
	"lanlink/internal/bus"
	"lanlink/internal/chat"
	"lanlink/internal/discovery"
	"lanlink/internal/transfer"
)

//go:embed Icon.png
var iconData []byte

var appIcon fyne.Resource

func init() {
	appIcon = fyne.NewStaticResource("icon", iconData)
}

const bcastKey = "broadcast"

var (
	fa      fyne.App
	win     fyne.Window
	core    *app.App
	tabs    *container.AppTabs
	selPeer string
	selName string
	inChat  bool // 是否处于聊天详情页（窗口内容被替换为 chatView）

	historyFile string // 历史消息文件
	histDirty   bool   // 内存会话已变更，需落盘

	// 消息栏
	convListBox  *fyne.Container
	transfersBox *fyne.Container

	// 用户栏
	usersContent *fyne.Container
	selfInfo     *widget.Label

	// 聊天详情页
	chatView   fyne.CanvasObject
	chatHeader *widget.Label
	chatBox    *fyne.Container
	chatScroll *container.Scroll
	chatEntry  *widget.Entry

	// 广播栏
	bcastBox    *fyne.Container
	bcastScroll *container.Scroll
	bcastEntry  *widget.Entry

	// convs 按对端（或 "broadcast" / 群组名）缓存聊天记录
	convs = map[string][]chat.Message{}

	// unread 每个会话的未读条数；peerNames 记住每个节点最近的昵称
	unread    = map[string]int{}
	peerNames = map[string]string{}

	// blinkOn 未读红点闪烁开关（由定时器翻转）
	blinkOn bool

	// darkMode 记录当前是否处于暗黑主题，用于解决 Android (gomobile) 上
	// theme.DarkTheme() 每次返回新实例导致 == 比较始终为 false 的 BUG。
	darkMode bool

	// downloadsDir 保存当前下载目录（App 外部存储），供 UI 显示给用户。
	downloadsDir string
)

// peerPalette 在线用户头像/昵称的彩色调色板（按节点 ID 哈希取色，稳定不变）
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

var grayColor = color.NRGBA{0x9E, 0x9E, 0x9E, 0xFF}

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

func main() {
	fa = fyneApp.NewWithID("com.kalaspace.lanlink")
	fa.SetIcon(appIcon)

	// Android 上必须在 main() 里直接创建并显示 UI；如果放到 OnStarted 回调中，
	// 该回调可能因 Activity 启动时序问题未被触发，导致应用永远卡在启动图标画面。
	dataDir, dlDir := appDirs()
	downloadsDir = dlDir
	historyFile = filepath.Join(dataDir, "history.json")
	loadHistory() // 启动即恢复历史消息
	cleanupSendTmp() // 启动清理上次的临时发送文件
	cfg := app.Default()
	cfg.DataDir = dataDir
	cfg.Downloads = downloadsDir

	var err error
	core, err = app.New(cfg)
	if err != nil {
		w := fa.NewWindow("LANLink")
		w.SetContent(widget.NewLabel("初始化失败\n\n" + err.Error()))
		w.Resize(fyne.NewSize(360, 260))
		w.Show()
		fa.Run()
		return
	}
	log.Println("LANLink: app.New OK, dataDir =", dataDir)

	win = fa.NewWindow("LANLink")
	win.SetMaster()
	win.Resize(fyne.NewSize(360, 640))
	buildUI()
	win.SetContent(tabs)
	win.Show()
	log.Println("LANLink: UI shown")

	// Activity 进入前台时启动后台网络服务
	fa.Lifecycle().SetOnStarted(func() {
		defer func() {
			if r := recover(); r != nil {
				crash(r, debug.Stack())
			}
		}()

		// 申请 MulticastLock：让 Android 不再丢弃局域网组播包，稳定双向发现
		AcquireMulticastLock()

		// 事件总线回调里会更新控件，必须用 fyne.Do 切回 UI 线程，
		// 否则在 Android 上直接改控件极易导致渲染崩溃（表现为"打开没反应"）。
		// Subscribe 返回 (chan, cancelFunc)，不存在失败情况。
		ch, _ := core.Bus.Subscribe(512)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					crash(r, debug.Stack())
				}
			}()
			for ev := range ch {
				ev := ev
				fyne.Do(func() { handleEvent(ev) })
			}
		}()

		// 未读红点闪烁定时器：有未读时每 600ms 翻转一次红点显隐
		go func() {
			tk := time.NewTicker(600 * time.Millisecond)
			for range tk.C {
				fyne.Do(func() {
					has := false
					for _, n := range unread {
						if n > 0 {
							has = true
							break
						}
					}
					if has {
						blinkOn = !blinkOn
						refreshConvs()
					} else if blinkOn {
						blinkOn = false
					}
				})
			}
		}()

		// 历史消息落盘定时器：有变更则每 2 秒保存一次
		go func() {
			tk := time.NewTicker(2 * time.Second)
			for range tk.C {
				fyne.Do(func() {
					if histDirty {
						histDirty = false
						saveHistory()
					}
				})
			}
		}()

		// core.Start 放到独立协程并兜底 recover，失败仅记日志 + 弹窗，不影响已显示的 UI
		go func() {
			defer func() {
				if r := recover(); r != nil {
					crash(r, debug.Stack())
					dialog.ShowError(fmt.Errorf("内核启动异常: %v", r), win)
				}
			}()
			core.Start()
			log.Println("LANLink: core.Start done")
		}()
	})

	// 应用被系统收回时优雅退出
	fa.Lifecycle().SetOnStopped(func() {
		if core != nil {
			core.Stop()
		}
		fyne.Do(saveHistory) // 应用收回前确保历史落盘
	})

	fa.Run()
}

// appDirs 获取移动端可靠的应用私有目录。
// Android 上 os.UserHomeDir() 常返回空，必须用 Fyne 提供的 Storage 根目录。
func appDirs() (dataDir, downloads string) {
	if fa != nil {
		if u := fa.Storage().RootURI(); u != nil && u.Path() != "" {
			root := u.Path()
			dataDir = filepath.Join(root, ".lanlink")
			// 优先使用外部存储（用户可通过 USB / 文件管理器访问），
			// 否则退回应用内部私有存储（/data/user/0/...，用户无法访问）。
			downloads = externalDownloadDir()
			if downloads == "" {
				downloads = filepath.Join(root, "LanLink")
			}
			return dataDir, downloads
		}
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, ".lanlink"), filepath.Join(home, "Downloads", "LanLink")
}

// externalDownloadDir 返回 Android 上应用"外部存储"中的下载目录。
// 形如 /storage/emulated/0/Android/data/com.kalaspace.lanlink/files/LanLink
// 它在 Android 11+ 仍由应用直接写（无需 MANAGE_EXTERNAL_STORAGE 权限），
// 并且用户用 USB 连电脑即可在 Android/data/com.kalaspace.lanlink/files/LanLink 找到。
func externalDownloadDir() string {
	cands := []string{
		os.Getenv("EXTERNAL_STORAGE"), // gomobile 在 Android 上通常设为 /storage/emulated/0
		"/storage/emulated/0",
		"/sdcard",
		"/mnt/sdcard",
	}
	for _, c := range cands {
		if c == "" {
			continue
		}
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return filepath.Join(c, "Android", "data", "com.kalaspace.lanlink", "files", "LanLink")
		}
	}
	return ""
}

// cleanupSendTmp 删除上一轮遗留在系统临时目录中的 llsend-* 临时发送文件。
func cleanupSendTmp() {
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "llsend-") {
			_ = os.Remove(filepath.Join(os.TempDir(), e.Name()))
		}
	}
}

// crash 记录致命错误/panic：同时打到 logcat（stderr 会被 gomobile 重定向到
// logcat，标签为 GoLog）并写入应用私有缓存，便于通过 adb 动态调试分析。
func crash(v interface{}, stack []byte) {
	msg := fmt.Sprintf("CRASH panic: %v\n%s", v, stack)
	log.Println(msg) // -> logcat (GoLog)
	_ = os.WriteFile(filepath.Join(os.TempDir(), "lanlink-crash.log"),
		[]byte(msg), 0644)
}

// ── UI 构建：QQ 式四栏（消息 / 用户 / 广播 / 设置）───────────

func buildUI() {
	// ── 消息栏 ──
	convListBox = container.NewVBox()
	transfersBox = container.NewVBox()
	histBtn := widget.NewButtonWithIcon("查看历史消息", theme.FileTextIcon(), viewHistory)
	msgsScroll := container.NewScroll(container.NewVBox(transfersBox, convListBox))
	msgsView := container.NewBorder(histBtn, nil, nil, nil, msgsScroll)

	// ── 用户栏 ──
	usersContent = container.NewVBox()
	selfInfo = widget.NewLabel("")
	usersView := container.NewScroll(usersContent)

	// ── 聊天详情页（不占 Tab，点击会话/用户后整窗切入）──
	chatHeader = widget.NewLabel("")
	chatHeader.TextStyle = fyne.TextStyle{Bold: true}
	chatBox = container.NewVBox()
	chatScroll = container.NewVScroll(chatBox)
	chatEntry = widget.NewEntry()
	chatEntry.SetPlaceHolder("输入消息，回车发送…")
	chatEntry.OnSubmitted = func(string) { sendMsg() }

	fileBtn := widget.NewButtonWithIcon("文件", theme.FileIcon(), pickAndSendFile)
	sendBtn := widget.NewButtonWithIcon("发送", theme.MailSendIcon(), sendMsg)
	chatInput := container.NewBorder(nil, nil, nil,
		container.NewHBox(fileBtn, sendBtn), chatEntry)
	backBtn := widget.NewButtonWithIcon("返回", theme.NavigateBackIcon(), closeChat)
	chatTop := container.NewBorder(nil, nil, backBtn, nil, chatHeader)
	chatView = container.NewBorder(chatTop, chatInput, nil, nil, chatScroll)

	// ── 广播栏 ──
	bcastBox = container.NewVBox()
	bcastScroll = container.NewVScroll(bcastBox)
	bcastEntry = widget.NewEntry()
	bcastEntry.SetPlaceHolder("向所有在线用户发布广播…")
	bcastEntry.OnSubmitted = func(string) { sendBcast() }
	bcastSend := widget.NewButtonWithIcon("发布", theme.MailSendIcon(), sendBcast)
	bcastInput := container.NewBorder(nil, nil, nil, bcastSend, bcastEntry)
	bcastTitle := widget.NewLabel("广播频道（发送给所有在线用户）")
	bcastTitle.TextStyle = fyne.TextStyle{Bold: true}
	bcastView := container.NewBorder(bcastTitle, bcastInput, nil, nil, bcastScroll)

	tabs = container.NewAppTabs(
		container.NewTabItemWithIcon("消息", theme.MailComposeIcon(), msgsView),
		container.NewTabItemWithIcon("用户", theme.AccountIcon(), usersView),
		container.NewTabItemWithIcon("广播", theme.VolumeUpIcon(), bcastView),
		container.NewTabItemWithIcon("设置", theme.SettingsIcon(), buildSettings()),
	)
	tabs.SetTabLocation(container.TabLocationBottom)
	tabs.OnSelected = func(ti *container.TabItem) {
		switch ti.Text {
		case "消息":
			refreshConvs()
		case "用户":
			refreshDevices()
		case "广播":
			unread[bcastKey] = 0
			refreshBcast()
			refreshConvs()
		}
	}

	refreshDevices()
	refreshConvs()
	refreshTransfers()
}

// ── 聊天详情页开关 ──────────────────────────────────────────

func openChat(key, name string) {
	selPeer, selName = key, name
	unread[key] = 0
	inChat = true
	chatHeader.SetText(name)
	refreshChat()
	win.SetContent(chatView)
	chatScroll.ScrollToBottom()
}

func closeChat() {
	inChat = false
	selPeer, selName = "", ""
	refreshConvs()
	win.SetContent(tabs)
}

// ── 消息栏 ──────────────────────────────────────────────────

// displayName 解析会话 key 的显示名：在线设备名 > 最近消息昵称 > 短 ID
func displayName(key string) string {
	if key == bcastKey {
		return "全体广播"
	}
	for _, p := range core.Disc.Peers() {
		if p.ID == key {
			return p.Name
		}
	}
	if n := peerNames[key]; n != "" {
		return n
	}
	return shortID(key)
}

func refreshConvs() {
	convListBox.Objects = nil

	type convRow struct {
		key  string
		last chat.Message
	}
	var rows []convRow
	for k, msgs := range convs {
		if len(msgs) == 0 {
			continue
		}
		rows = append(rows, convRow{key: k, last: msgs[len(msgs)-1]})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].last.TS > rows[j].last.TS })

	if len(rows) == 0 {
		convListBox.Add(widget.NewLabel("（暂无消息，去「用户」页找人聊天吧）"))
		convListBox.Refresh()
		return
	}

	for _, r := range rows {
		r := r
		name := displayName(r.key)
		preview := r.last.Text
		if pr := []rune(preview); len(pr) > 18 {
			preview = string(pr[:18]) + "…"
		}
		dot := ""
		if n := unread[r.key]; n > 0 {
			if blinkOn {
				dot = fmt.Sprintf("🔴%d ", n)
			} else {
				dot = fmt.Sprintf("⭕%d ", n)
			}
		}
		btn := widget.NewButton(fmt.Sprintf("%s%s：%s", dot, name, preview), func() {
			if r.key == bcastKey {
				unread[bcastKey] = 0
				tabs.SelectIndex(2)
				refreshConvs()
				return
			}
			openChat(r.key, name)
		})
		btn.Alignment = widget.ButtonAlignLeading
		convListBox.Add(btn)
	}
	convListBox.Refresh()
}

// ── 用户栏 ──────────────────────────────────────────────────

// avatarObj 生成圆形头像：在线为节点专属彩色，离线为灰色
func avatarObj(name string, col color.NRGBA) fyne.CanvasObject {
	c := canvas.NewCircle(col)
	initial := "?"
	for _, r := range name {
		initial = string(r)
		break
	}
	t := canvas.NewText(initial, color.White)
	t.TextStyle = fyne.TextStyle{Bold: true}
	t.TextSize = 16
	size := canvas.NewRectangle(color.Transparent)
	size.SetMinSize(fyne.NewSize(42, 42))
	return container.NewStack(size, c, container.NewCenter(t))
}

func refreshDevices() {
	usersContent.Objects = nil
	selfInfo.SetText(fmt.Sprintf("本机昵称：%s\n节点 ID：%s", core.ID.Name(), shortID(core.ID.ID)))
	usersContent.Add(widget.NewCard("我的设备", "", selfInfo))

	usersContent.Add(widget.NewButton("🔄 刷新设备列表", func() {
		core.Disc.Refresh()
		// 等一秒等对端响应回来后再刷新 UI
		time.AfterFunc(1500*time.Millisecond, func() {
			fyne.Do(refreshDevices)
		})
	}))

	peers := core.Disc.Peers()
	// 在线在前、离线在后，各按名称排序
	sort.Slice(peers, func(i, j int) bool {
		if peers[i].Online != peers[j].Online {
			return peers[i].Online
		}
		return peers[i].Name < peers[j].Name
	})

	onlineCount := 0
	for _, p := range peers {
		if p.Online {
			onlineCount++
		}
		p := p
		peerNames[p.ID] = p.Name

		// 头像与昵称：在线=彩色，离线=灰色（与 PC 端一致）
		col := grayColor
		nameCol := color.Color(grayColor)
		suffix := "（离线）"
		if p.Online {
			col = peerColor(p.ID)
			nameCol = theme.Color(theme.ColorNameForeground)
			suffix = ""
		}
		av := avatarObj(p.Name, col)
		nameText := canvas.NewText(p.Name+suffix, nameCol)
		nameText.TextStyle = fyne.TextStyle{Bold: true}
		nameText.TextSize = 15
		ipText := canvas.NewText(p.IP.String(), grayColor)
		ipText.TextSize = 12
		info := container.NewVBox(nameText, ipText)

		chatBtn := widget.NewButtonWithIcon("", theme.MailComposeIcon(), func() {
			openChat(p.ID, p.Name)
		})
		delBtn := widget.NewButtonWithIcon("", theme.DeleteIcon(), func() {
			core.RemovePeer(p.ID)
			delete(convs, p.ID)
			delete(unread, p.ID)
			if selPeer == p.ID {
				selPeer, selName = "", ""
			}
			refreshDevices()
			refreshConvs()
		})

		row := container.NewBorder(nil, nil,
			av, container.NewHBox(chatBtn, delBtn),
			container.NewPadded(info))
		usersContent.Add(row)
		usersContent.Add(widget.NewSeparator())
	}
	if len(peers) == 0 {
		usersContent.Add(widget.NewLabel("（局域网内暂无其他设备）"))
	} else if onlineCount == 0 {
		usersContent.Add(widget.NewLabel("（所有设备均已离线，可发离线消息，对方上线自动送达）"))
	}
	usersContent.Refresh()
}

// ── 聊天详情 ────────────────────────────────────────────────

func sendMsg() {
	text := strings.TrimSpace(chatEntry.Text)
	if text == "" || selPeer == "" {
		return
	}
	ts := time.Now().UnixMilli()
	if err := core.Chat.SendText(context.Background(), selPeer, text); err != nil {
		if errors.Is(err, chat.ErrPeerOffline) {
			// 离线消息：已进入离线队列，对方上线后自动补发
			m := chat.Message{TS: ts, From: core.ID.ID, FromName: core.ID.Name(), To: selPeer, Text: "[离线暂存] " + text, Outgoing: true}
			convs[selPeer] = append(convs[selPeer], m)
			histDirty = true
			chatEntry.SetText("")
			refreshChat()
			return
		}
		dialog.ShowError(err, win)
		return
	}
	m := chat.Message{TS: ts, From: core.ID.ID, FromName: core.ID.Name(), To: selPeer, Text: text, Outgoing: true}
	convs[selPeer] = append(convs[selPeer], m)
	histDirty = true
	chatEntry.SetText("")
	refreshChat()
}

func pickAndSendFile() {
	if selPeer == "" || selPeer == bcastKey {
		dialog.ShowInformation("提示", "请先选择一个对端再发送文件。", win)
		return
	}
	dialog.ShowFileOpen(func(r fyne.URIReadCloser, e error) {
		if e != nil {
			dialog.ShowError(e, win)
			return
		}
		if r == nil {
			return
		}
		// Android 上文件选择器返回 content:// URI，r.URI().Path() 不是真实
		// 文件路径，直接用会导致 buildManifest 打开失败。先复制到临时文件，
		// 再用真实路径发起传输。
		ext := filepath.Ext(r.URI().Name())
		tmp, err := os.CreateTemp("", "llsend-*"+ext)
		if err != nil {
			dialog.ShowError(err, win)
			_ = r.Close()
			return
		}
		tmpPath := tmp.Name()
		_, err = io.Copy(tmp, r)
		tmp.Close()
		_ = r.Close()
		if err != nil {
			dialog.ShowError(err, win)
			_ = os.Remove(tmpPath)
			return
		}
		go func() {
			if _, err := core.Transfer.SendPaths(context.Background(), selPeer, []string{tmpPath}); err != nil {
				dialog.ShowError(err, win)
				_ = os.Remove(tmpPath)
			}
			// 注意：发送成功后不能立即删除 tmpPath，接收方会在 TCP 连入后
			// 才拉取真实文件数据，那时才真正读取该临时文件（启动时统一清理）。
		}()
	}, win)
}

func refreshChat() {
	chatBox.Objects = nil
	msgs := convs[selPeer]
	if len(msgs) == 0 {
		chatBox.Add(widget.NewLabel("（暂无聊天记录，发送第一条消息吧）"))
	}
	for _, m := range msgs {
		chatBox.Add(bubble(m))
	}
	chatBox.Refresh()
	chatScroll.ScrollToBottom()
}

// ── 广播栏 ──────────────────────────────────────────────────

func sendBcast() {
	text := strings.TrimSpace(bcastEntry.Text)
	if text == "" {
		return
	}
	if err := core.Chat.Broadcast(text); err != nil {
		dialog.ShowError(err, win)
		return
	}
	m := chat.Message{TS: time.Now().UnixMilli(), From: core.ID.ID, FromName: core.ID.Name(), Text: text, Bcast: true, Outgoing: true}
	convs[bcastKey] = append(convs[bcastKey], m)
	bcastEntry.SetText("")
	refreshBcast()
	refreshConvs()
}

func refreshBcast() {
	bcastBox.Objects = nil
	msgs := convs[bcastKey]
	if len(msgs) == 0 {
		bcastBox.Add(widget.NewLabel("（暂无广播消息）"))
	}
	for _, m := range msgs {
		bcastBox.Add(bubble(m))
	}
	bcastBox.Refresh()
	bcastScroll.ScrollToBottom()
}

// ── 气泡渲染 ────────────────────────────────────────────────

func bubble(m chat.Message) fyne.CanvasObject {
	who := m.FromName
	if m.Outgoing {
		who = "我"
	}
	// 不做任何容器包裹（HBox/Border）—— Android (gomobile) 上包裹容器
	// 都会按 Label 的错误 MinSize 来分配宽度，造成文字截断。
	// 直接返回 Label，让 VBox 给其填满宽度；通过 Alignment 实现左右对齐。
	label := widget.NewLabel(who + "：" + m.Text)
	label.Selectable = true // 允许在聊天气泡中选择并复制文本
	label.Wrapping = fyne.TextWrapBreak // Break 对中文更友好
	if m.Outgoing {
		label.Alignment = fyne.TextAlignTrailing
	}
	return label
}

// ── 历史消息（持久化 + 查看）────────────────────────────────

// truncate 截断字符串到 n 个 rune（用于历史列表预览）
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// loadHistory 从磁盘读取历史消息并合并进内存会话
func loadHistory() {
	b, err := os.ReadFile(historyFile)
	if err != nil {
		return
	}
	data := map[string][]chat.Message{}
	if err := json.Unmarshal(b, &data); err != nil {
		return
	}
	for k, msgs := range data {
		if len(msgs) > 0 {
			convs[k] = msgs
		}
	}
}

// saveHistory 将内存会话全量写入磁盘（先写临时文件再改名，避免写一半损坏）
func saveHistory() {
	snap := map[string][]chat.Message{}
	for k, v := range convs {
		snap[k] = append([]chat.Message(nil), v...)
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return
	}
	tmp := historyFile + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, historyFile)
}

// viewHistory 弹出历史消息浏览窗口：上方面板为会话列表，下方为完整消息气泡
func viewHistory() {
	keys := make([]string, 0, len(convs))
	for k := range convs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		dialog.ShowInformation("历史消息", "（暂无历史记录）", win)
		return
	}

	msgView := container.NewVBox()
	scroll := container.NewScroll(msgView)
	scroll.SetMinSize(fyne.NewSize(320, 360))

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
			msgs := convs[k]
			title.SetText(displayName(k))
			if len(msgs) > 0 {
				last := msgs[len(msgs)-1]
				who := last.FromName
				if last.Outgoing {
					who = "我"
				}
				prev.SetText(fmt.Sprintf("[%s] %s：%s",
					time.UnixMilli(last.TS).Format("15:04:05"), who, truncate(last.Text, 24)))
			} else {
				prev.SetText("（无消息）")
			}
			title.Refresh()
			prev.Refresh()
		},
	)
	list.OnSelected = func(id widget.ListItemID) {
		k := keys[id]
		msgs := convs[k]
		msgView.Objects = nil
		if len(msgs) == 0 {
			msgView.Add(widget.NewLabel("（无消息）"))
		}
		for _, m := range msgs {
			msgView.Add(bubble(m))
		}
		msgView.Refresh()
		scroll.ScrollToTop()
	}

	content := container.NewBorder(list, nil, nil, nil, scroll)
	d := dialog.NewCustom("历史消息", "关闭", content, win)
	d.Resize(fyne.NewSize(360, 560))
	d.Show()
}

// ── 事件处理 ────────────────────────────────────────────────

func handleEvent(ev bus.Event) {
	switch ev.Topic {
	case bus.TopicPeerOnline, bus.TopicPeerOffline:
		refreshDevices()
		refreshConvs()
	case bus.TopicMsgRecv:
		if m, ok := ev.Data.(chat.Message); ok {
			appendConvsMsg(m, false)
		}
	case bus.TopicMsgSent, bus.TopicMsgQueued:
		if m, ok := ev.Data.(chat.Message); ok {
			appendConvsMsg(m, true)
		}
	case bus.TopicMsgFailed:
		if m, ok := ev.Data.(chat.Message); ok {
			dialog.ShowInformation("发送失败", "消息未能送达："+m.Text, win)
		}
	case bus.TopicTransferOffer:
		refreshTransfers()
		if inChat {
			dialog.ShowInformation("文件传输", "收到文件请求，请返回「消息」页处理。", win)
		} else {
			tabs.SelectIndex(0)
		}
	case bus.TopicTransferProgress, bus.TopicTransferDone, bus.TopicTransferPaused, bus.TopicTransferError:
		refreshTransfers()
	case bus.TopicPeerWarn:
		if pe, ok := ev.Data.(discovery.PeerEvent); ok {
			dialog.ShowInformation("安全告警", "对端身份可能已变更："+pe.Name, win)
		}
	}
}

// appendConvsMsg 去重追加聊天记录（sendMsg 已手动追加过，事件到达时跳过重复项）
func appendConvsMsg(m chat.Message, outgoing bool) {
	key := msgKey(m, outgoing)
	if !outgoing && m.FromName != "" {
		peerNames[m.From] = m.FromName
	}
	msgs := convs[key]
	// 与上一条内容、发送者、方向全相同则视为重复
	if len(msgs) > 0 {
		last := msgs[len(msgs)-1]
		if last.Text == m.Text && last.From == m.From && last.Outgoing == m.Outgoing {
			return
		}
		// 离线暂存的本地回显（带前缀），事件补发时也视为重复
		if last.Outgoing && m.Outgoing && last.Text == "[离线暂存] "+m.Text {
			return
		}
	}
	convs[key] = append(convs[key], m)
	histDirty = true

	if key == bcastKey {
		refreshBcast()
	}
	if inChat && selPeer == key {
		refreshChat()
		return
	}
	// 不在该会话界面：累计未读（仅对收到的消息），红点由定时器闪烁
	if !outgoing {
		unread[key]++
	}
	refreshConvs()
}

func msgKey(m chat.Message, outgoing bool) string {
	if m.Bcast {
		return bcastKey
	}
	if m.Group != "" {
		return m.Group
	}
	if outgoing {
		return m.To
	}
	return m.From
}

// ── 传输任务（嵌在消息栏顶部）────────────────────────────────

func refreshTransfers() {
	transfersBox.Objects = nil
	tasks := core.Transfer.Tasks()
	if len(tasks) == 0 {
		transfersBox.Refresh()
		return
	}
	for _, t := range tasks {
		transfersBox.Add(taskRow(t))
	}
	transfersBox.Refresh()
}

func taskRow(t transfer.TaskView) fyne.CanvasObject {
	dir := "↑ 发送"
	if t.Dir == transfer.DirRecv {
		dir = "↓ 接收"
	}
	title := fmt.Sprintf("%s  %s", dir, t.PeerName)
	sub := fmt.Sprintf("%s  %d/%d 文件", stateText(t.State), t.Done, t.Total)
	if t.SpeedBps > 0 {
		sub += "  " + humanRate(t.SpeedBps)
	}
	row := container.NewVBox(
		widget.NewLabel(title),
		widget.NewLabel(sub),
	)
	bar := widget.NewProgressBar()
	if t.Total > 0 {
		bar.SetValue(float64(t.Done) / float64(t.Total))
	}
	row.Add(bar)

	switch t.State {
	case transfer.StatePendingLocal:
		row.Add(container.NewHBox(
			widget.NewButton("接收", func() { _ = core.Transfer.Accept(context.Background(), t.ID) }),
			widget.NewButton("拒绝", func() { _ = core.Transfer.Reject(context.Background(), t.ID, "用户拒绝") }),
		))
	case transfer.StateTransferring:
		row.Add(container.NewHBox(widget.NewButton("暂停", func() { _ = core.Transfer.Pause(t.ID) })))
	case transfer.StatePaused:
		row.Add(container.NewHBox(widget.NewButton("继续", func() { _ = core.Transfer.Resume(context.Background(), t.ID) })))
	case transfer.StateCompleted:
		if t.Dir == transfer.DirRecv {
			row.Add(container.NewHBox(widget.NewButton("位置", func() {
				paths, err := core.Transfer.SavedPaths(t.ID)
				if err == nil && len(paths) > 0 {
					dialog.ShowInformation("已保存", strings.Join(paths, "\n"), win)
				} else {
					dialog.ShowInformation("已保存", core.Transfer.Downloads(), win)
				}
			})))
		}
	}
	return widget.NewCard("", "", row)
}

// ── 设置栏 ──────────────────────────────────────────────────

func buildSettings() fyne.CanvasObject {
	nameEntry := widget.NewEntry()
	nameEntry.SetText(core.ID.Name())
	nameEntry.SetPlaceHolder("本机昵称")

	saveName := widget.NewButton("保存昵称", func() {
		n := strings.TrimSpace(nameEntry.Text)
		if n == "" {
			return
		}
		core.ID.SetName(n)
		dialog.ShowInformation("已保存", "昵称已更新为 "+n+"（下次心跳后对其他设备生效）", win)
		refreshDevices()
	})

	udpLabel := widget.NewLabel(fmt.Sprintf("UDP 发现端口：%d", core.Cfg.UDPPort))
	tcpLabel := widget.NewLabel(fmt.Sprintf("TCP 传输端口：%d", core.Cfg.TCPPort))
	dlLabel := widget.NewLabel("接收目录：" + core.Transfer.Downloads())
	dlLabel.Wrapping = fyne.TextWrapBreak

	darkBtn := widget.NewButton("切换深色 / 浅色主题", func() {
		darkMode = !darkMode
		if darkMode {
			fa.Settings().SetTheme(theme.DarkTheme())
		} else {
			fa.Settings().SetTheme(theme.LightTheme())
		}
	})

	about := widget.NewLabel("LANLink 内网通联 · 手机端\n基于 Go + Fyne，与电脑端共用同一套加密与协议。\nX：@kalaspace002")
	about.Wrapping = fyne.TextWrapWord

	return container.NewScroll(container.NewVBox(
		widget.NewCard("身份", "", container.NewVBox(nameEntry, saveName)),
		widget.NewCard("网络", "", container.NewVBox(udpLabel, tcpLabel, dlLabel)),
		widget.NewCard("外观", "", darkBtn),
		widget.NewCard("关于", "", about),
	))
}

func stateText(s transfer.State) string {
	switch s {
	case transfer.StateOffering:
		return "等待对端确认"
	case transfer.StatePendingLocal:
		return "待接收"
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
	default:
		return string(s)
	}
}

func humanRate(bps float64) string {
	if bps < 1024 {
		return fmt.Sprintf("%.0f B/s", bps)
	}
	if bps < 1024*1024 {
		return fmt.Sprintf("%.1f KB/s", bps/1024)
	}
	return fmt.Sprintf("%.2f MB/s", bps/1024/1024)
}

func shortID(id string) string {
	if len(id) <= 16 {
		return id
	}
	return id[:8] + "…" + id[len(id)-4:]
}
