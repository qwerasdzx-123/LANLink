<div align="center">

<img src="./cmd/lanlink-gui/assets/icon.png" alt="LANLink" width="160">

# 🔗 LANLink · 内网通联工具

### 局域网点对点即时通讯与文件传输 — 无需服务器，开箱即用

[![Go](https://img.shields.io/badge/Go-1.22-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Platform](https://img.shields.io/badge/Platform-Windows%20%7C%20Android-0078D6?logo=windows&logoColor=white)](https://www.microsoft.com)
[![GUI](https://img.shields.io/badge/GUI-Fyne%20v2.8-41b883?logo=go&logoColor=white)](https://fyne.io)
[![Encryption](https://img.shields.io/badge/Encryption-AES--256--GCM-blue)](#security)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](./LICENSE)

**LANLink** is a serverless LAN peer-to-peer messenger & file transfer tool — auto-discover peers, chat, and share files at full local-network speed, all end-to-end encrypted.

[📥 下载](https://github.com/qwerasdzx-123/LANLink/releases) · [🚀 快速开始](#quick-start) · [🔐 安全机制](#security) · [📁 项目结构](#structure) · [𝕏 @kalaspace002](https://x.com/kalaspace002)

</div>

---

## 🆕 4.0 版本亮点 · What's New in v4.0

| 特性 | 说明 | 适用端 |
|------|------|--------|
| 💾 **历史消息持久化** | 所有会话消息自动写入本地 `history.json`，重启后自动恢复，不再丢失 | Windows + Android |
| 🗂️ **历史消息查看** | 聊天界面新增「历史记录 / 查看历史消息」按钮，可浏览**全部会话**（含已离线、已删除设备的历史） | Windows + Android |
| 🏷️ **昵称永久保存** | 昵称随身份写入 `identity.json`，重启后不再回退为初始主机名 | Windows + Android |
| 📋 **聊天文本可选中复制** | 聊天气泡文本支持鼠标拖拽选中 + `Ctrl+C` 复制 + 右键「复制」 | Windows + Android |
| 📱 **Android 客户端** | 新增官方 APK 客户端 `LANLink.apk`，手机端同样支持发现、单聊、群聊、广播、文件传输与历史 | Android |

---

## ✨ 核心特性 · Core Features

### 🔍 **局域网自动发现（Auto Discovery）**
- 基于 UDP 广播的在线 / 心跳 / 离线通告，节点上线 2 秒内互见
- 通告携带 **Ed25519 签名**，伪造身份会被自动丢弃并触发安全告警
- 同一网段多机自动组网，无需任何中心服务器

### 🔒 **端到端加密（End-to-End Encryption）**
- 握手：Ed25519 签名绑定的 X25519 临时密钥交换，防中间人
- 派生：HKDF-SHA256 生成双向 32 字节会话密钥
- 传输：AES-256-GCM 认证加密，密文被篡改即解密失败

### 💬 **即时通讯（Realtime Chat）**
- 文字、图片、表情消息，局域网内低延迟送达
- **广播消息** 📢 一键发给所有在线用户
- **群聊** 👥 自由建群、勾选成员，支持多人群组会话
- 新消息提示音 + 托盘闪烁 + 可配置弹窗提醒
- **文本可选中复制**：在任意聊天气泡中拖拽选中文字，`Ctrl+C` 或右键即可复制
- **历史消息自动保存**：消息实时落盘，重启不丢失；点击「历史记录」可回溯任意会话（含离线设备）

### 📁 **高速文件传输（File Transfer）**
- 走 TCP 直连，跑满局域网带宽，大文件传输飞快
- 支持**断点续传**与**暂停 / 继续**，传输中断不丢进度
- 多文件批量发送，自动计算大小与实时速率
- 接收方可**接受 / 拒绝**，传输列表实时进度条

### 🖱️ **接收即打开（Open on Receive）**
- 每条已完成接收任务提供 **「打开」** 与 **「打开目录」** 两个按钮
- 设置中可开启 **「接收完成后自动打开文件」**，收完即用默认程序打开

### 🛠️ **个性化与易用（Customization）**
- 系统托盘常驻，关闭按钮可最小化到托盘
- 自定义昵称、头像、备注名，隐藏 IP 地址
- **昵称永久保存**：修改后写入身份文件，重启不再恢复初始昵称
- 开机自启动、暗黑主题、提示音一键开关
- 发送限速（KB/s），避免占用全部带宽

### 📱 **Android 客户端（v4.0 新增）**
- 与 Windows 端共用同一套加密与发现协议，跨平台互通
- 支持单聊、群聊、广播、文件收发、历史消息查看
- 触摸友好的消息气泡与历史浏览界面

---

## 📋 环境要求 · Requirements

| 项目 | 说明 |
|------|------|
| **Windows** | Windows 10 / 11，单文件 `lanlink-gui.exe` 即可运行 |
| **Android** | Android 8.0+，安装 `LANLink.apk`（需允许「未知来源」安装） |
| **运行依赖** | 无第三方运行时，原生编译产物 |
| **网络** | 同一局域网（两端均需放行对应 UDP / TCP 端口） |
| **编译环境** | Go ≥ 1.22（如需从源码构建） |

---

<a id="quick-start"></a>

## 🚀 快速开始 · Quick Start

### Windows 端（方式一：直接运行，推荐）
1. 下载 `lanlink-gui.exe`（或本仓库构建产物 `LANlink-4.0.exe`）
2. **双击运行**，首次启动自动生成身份并广播上线
3. 同一局域网内的其他节点会在左侧列表中自动出现
4. 点击节点 → 发消息 / 拖文件即可开始传输

> 💡 提示：若双方无法互见，请确认 UDP 信令端口一致（`-udp`），且防火墙已放行 `lanlink-gui.exe`。

### Android 端（方式二：安装 APK）
1. 下载 `LANLink.apk` 并安装，安装时允许「未知来源」应用
2. 首次启动自动生成身份并广播上线
3. 在「消息」页可看到同一局域网内的在线节点并开始聊天 / 传文件
4. 点「消息」页顶部「查看历史消息」可浏览全部历史会话

### 方式三：从源码构建
```bash
# 克隆仓库
git clone https://github.com/qwerasdzx-123/LANLink.git
cd LANLink

# 构建 Windows GUI 版（无控制台窗口）
go build -ldflags "-H windowsgui" -o LANlink-4.0.exe ./cmd/lanlink-gui

# 构建 Android APK（需配置 Android SDK / NDK 与 gomobile）
# 详见 cmd/lanlink-android/AndroidManifest.xml，通常用项目内 build-apk.bat
```

---

## ⚙️ 命令行参数 · CLI Flags（Windows 端）

`lanlink-gui.exe` 支持以下启动参数：

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-name` | 主机名 | 显示昵称（修改后会持久化到身份文件） |
| `-udp`  | `45450` | UDP 信令端口（用于局域网发现） |
| `-tcp`  | `45451` | TCP 数据端口（`0` = 自动分配） |
| `-data` | 用户目录 | 数据目录（身份、设置、头像、历史消息） |
| `-dl`   | `下载/LANLink` | 文件接收目录 |

示例：
```bash
lanlink-gui.exe -name Alice
```

---

<a id="structure"></a>

## 🗂️ 项目结构 · Project Structure

```
LANLink/
├── cmd/
│   ├── lanlink-gui/            # Windows GUI 版入口（Fyne）
│   │   ├── main.go             # 界面装配、生命周期、事件总线订阅
│   │   ├── settings.go         # 设置结构（持久化到 settings.json）
│   │   ├── history.go          # 历史消息持久化与加载（v4.0 新增）
│   │   └── assets/             # 图标资源
│   └── lanlink-android/        # Android 客户端入口（v4.0 新增）
│       ├── main.go             # Android 界面与历史消息（v4.0 新增）
│       ├── AndroidManifest.xml # Android 权限与配置
│       ├── multicast_*.go      # 安卓多播兼容层
│       └── Icon.png            # 应用图标
│
├── internal/                   # 核心模块（与界面解耦，可复用）
│   ├── app/                    # 应用编排：装配各模块、配置
│   ├── identity/               # 节点身份：Ed25519 密钥对、签名/验签、昵称持久化（v4.0）
│   ├── discovery/              # 局域网发现：UDP 广播 + 签名通告
│   ├── transport/              # 传输层：UDP 信令 + TCP 数据
│   ├── protocol/               # 协议帧：CBOR 序列化
│   ├── secure/                 # 加密会话：X25519 + HKDF + AES-256-GCM
│   ├── chat/                   # 聊天：单聊 / 群聊 / 广播
│   ├── transfer/               # 文件传输：分块、续传、限速、管理器
│   └── bus/                    # 事件总线：模块间解耦通信
│
├── go.mod / go.sum             # Go 模块依赖
├── build-gui.bat / build-apk.bat  # Windows / Android 构建脚本
└── README.md
```

**技术栈**：Go 1.22 · Fyne v2.8 · CBOR · X25519 / Ed25519 / AES-256-GCM · UDP + TCP

---

<a id="security"></a>

## 🔐 安全机制 · Security

LANLink 在设计上假设局域网不可完全信任，提供多层防护：

| 层级 | 机制 | 作用 |
|------|------|------|
| 身份 | Ed25519 密钥对 | 每个节点拥有不可伪造的稳定身份 |
| 发现 | 签名通告 | 伪造 / 篡改的通告被丢弃，并触发安全告警 |
| 握手 | X25519 临时交换 + Ed25519 签名 | 绑定身份，阻止中间人冒充 |
| 会话 | HKDF-SHA256 派生双向密钥 | 每条连接独立密钥 |
| 传输 | AES-256-GCM | 保密 + 完整性，篡改即失败 |

> ⚠️ 注意：加密用于保护传输内容，仍依赖双方身份可信。请勿在不可信网络暴露信令端口。
> 本地历史消息以明文 `history.json` 存储于本机数据目录，仅限本机读取，请妥善保管设备。

---

## 🔧 常见问题 · FAQ

### Q1：为什么看不到对方？
A：请确认双方处于**同一局域网**且 UDP 信令端口一致（默认 `45450`）。防火墙可能拦截 UDP 广播，请允许 `lanlink-gui.exe` 通过防火墙；Android 端需授予「局域网 / 后台网络」相关权限。

### Q2：文件传输速度慢？
A：速度取决于局域网带宽。可在设置中调整发送限速；关闭限速（0）即可跑满带宽。大文件建议保持默认，支持断点续传。

### Q3：传输中断会丢失文件吗？
A：不会。传输支持**断点续传**与**暂停 / 继续**，重新连接后可从断点继续，已接收部分不会丢失。

### Q4：聊天内容会被窃听吗？
A：所有点对点消息与文件均经 **AES-256-GCM** 端到端加密，局域网内第三方无法解密。

### Q5：如何更换头像 / 昵称？
A：在「设置」面板中点击头像可更换图片，昵称、备注、网段等均可在此修改。自 **v4.0** 起，昵称修改后会持久化到身份文件，重启不再恢复初始昵称。

### Q6：重启后聊天记录没了？
A：自 **v4.0** 起已默认开启历史消息持久化，重启会自动恢复。若仍为空，请确认数据目录下的 `history.json` 未被手动删除。点击聊天界面「历史记录」按钮可随时回溯。

### Q7：如何复制聊天里的文字？
A：在聊天气泡中用鼠标拖拽选中文字，按 `Ctrl+C` 或右键选择「复制」即可（v4.0 新增）。

---

## 📈 后续规划 · Roadmap

- 🔥 **高优先级**：跨网段中继 / 穿透、传输完整性校验（哈希）
- ⚡ **中优先级**：消息历史云同步、文件缩略图预览、拖拽发送优化
- 🛠️ **低优先级**：macOS / Linux 原生构建、国际化（多语言）、主题自定义

---

## 🤝 贡献 · Contributing

欢迎 Issue 与 Pull Request！

1. **报告问题**：在 [Issues](../../issues) 中详细描述复现步骤
2. **功能建议**：说明使用场景与预期效果
3. **代码贡献**：
   - Fork 本仓库
   - 创建特性分支 `git checkout -b feature/AmazingFeature`
   - 提交改动 `git commit -m 'Add some AmazingFeature'`
   - 推送到分支 `git push origin feature/AmazingFeature`
   - 发起 Pull Request

---

## 📄 开源协议 · License

本项目基于 [MIT License](./LICENSE) 开源。

**Made with ❤️ by Kalaspace**
