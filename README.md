<div align="center">

# 🔗 LANLink · 内网通联工具

### 局域网点对点即时通讯与文件传输 — 无需服务器，开箱即用

[![Go](https://img.shields.io/badge/Go-1.22-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![Platform](https://img.shields.io/badge/Platform-Windows-0078D6?logo=windows&logoColor=white)](https://www.microsoft.com)
[![GUI](https://img.shields.io/badge/GUI-Fyne%20v2.8-41b883?logo=go&logoColor=white)](https://fyne.io)
[![Encryption](https://img.shields.io/badge/Encryption-AES--256--GCM-blue)](#-安全机制-security)
[![License](https://img.shields.io/badge/License-MIT-green.svg)](./LICENSE)

**LANLink** is a serverless LAN peer-to-peer messenger & file transfer tool — auto-discover peers, chat, and share files at full local-network speed, all end-to-end encrypted.

[📥 下载](#-快速开始-quick-start) · [🚀 快速开始](#-快速开始-quick-start) · [🔐 安全机制](#-安全机制-security) · [📁 项目结构](#-项目结构-project-structure) · [𝕏 @kalaspace002](https://x.com/kalaspace002)

</div>

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
- 开机自启动、暗黑主题、提示音一键开关
- 发送限速（KB/s），避免占用全部带宽

---

## 📋 环境要求 · Requirements

| 项目 | 说明 |
|------|------|
| **操作系统** | Windows 10 / 11（GUI 版，Fyne 跨平台，可自行编译到 macOS / Linux） |
| **运行依赖** | 无，单文件 `lanlink-gui.exe` 即可运行 |
| **网络** | 同一局域网（或配置 `-bind` 指定网卡） |
| **编译环境** | Go ≥ 1.22（如需从源码构建） |

---

## 🚀 快速开始 · Quick Start

### 方式一：直接运行（推荐）
1. 下载 `lanlink-gui.exe`
2. **双击运行**，首次启动自动生成身份并广播上线
3. 同一局域网内的其他节点会在左侧列表中自动出现
4. 点击节点 → 发消息 / 拖文件即可开始传输

> 💡 提示：若双方无法互见，请确认 UDP 信令端口一致（`-udp`），且防火墙已放行 `lanlink-gui.exe`。

### 方式二：从源码构建
```bash
# 克隆仓库
git clone https://github.com/<你的用户名>/lanlink.git
cd lanlink

# 构建 GUI 版（Windows，无控制台窗口）
go build -ldflags "-H windowsgui" -o lanlink-gui.exe ./cmd/lanlink-gui
```

---

## ⚙️ 命令行参数 · CLI Flags

`lanlink-gui.exe` 支持以下启动参数：

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `-name` | 主机名 | 显示昵称 |
| `-udp`  | `45450` | UDP 信令端口（用于局域网发现） |
| `-tcp`  | `45451` | TCP 数据端口（`0` = 自动分配） |
| `-data` | 用户目录 | 数据目录（身份、设置、头像） |
| `-dl`   | `下载/LANLink` | 文件接收目录 |

示例：
```bash
lanlink-gui.exe -name Alice
```

---

## 🗂️ 项目结构 · Project Structure

```
lanlink/
├── cmd/
│   └── lanlink-gui/            # GUI 版入口（Fyne）
│       ├── main.go             # 界面装配、生命周期、事件总线订阅
│       ├── settings.go         # 设置结构（持久化到 settings.json）
│       └── assets/             # 图标资源
│
├── internal/                   # 核心模块（与界面解耦，可复用）
│   ├── app/                    # 应用编排：装配各模块、配置
│   ├── identity/               # 节点身份：Ed25519 密钥对、签名/验签
│   ├── discovery/              # 局域网发现：UDP 广播 + 签名通告
│   ├── transport/              # 传输层：UDP 信令 + TCP 数据
│   ├── protocol/               # 协议帧：CBOR 序列化
│   ├── secure/                 # 加密会话：X25519 + HKDF + AES-256-GCM
│   ├── chat/                   # 聊天：单聊 / 群聊 / 广播
│   ├── transfer/               # 文件传输：分块、续传、限速、管理器
│   └── bus/                    # 事件总线：模块间解耦通信
│
├── go.mod / go.sum             # Go 模块依赖
└── README.md
```

**技术栈**：Go 1.22 · Fyne v2.8 · CBOR · X25519 / Ed25519 / AES-256-GCM · UDP + TCP

---

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

---

## 🔧 常见问题 · FAQ

### Q1：为什么看不到对方？
A：请确认双方处于**同一局域网**且 UDP 信令端口一致（默认 `45450`）。防火墙可能拦截 UDP 广播，请允许 `lanlink-gui.exe` 通过防火墙。

### Q2：文件传输速度慢？
A：速度取决于局域网带宽。可在设置中调整发送限速；关闭限速（0）即可跑满带宽。大文件建议保持默认，支持断点续传。

### Q3：传输中断会丢失文件吗？
A：不会。传输支持**断点续传**与**暂停 / 继续**，重新连接后可从断点继续，已接收部分不会丢失。

### Q4：聊天内容会被窃听吗？
A：所有点对点消息与文件均经 **AES-256-GCM** 端到端加密，局域网内第三方无法解密。

### Q5：如何更换头像 / 昵称？
A：在「设置」面板中点击头像可更换图片，昵称、备注、网段等均可在此修改并即时生效。

---

## 📈 后续规划 · Roadmap

- 🔥 **高优先级**：跨网段中继 / 穿透、传输完整性校验（哈希）
- ⚡ **中优先级**：消息历史持久化、文件缩略图预览、拖拽发送优化
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
