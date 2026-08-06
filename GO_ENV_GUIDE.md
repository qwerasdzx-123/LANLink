# Go 语言环境应用指南（LANLink 项目专用）

> 适用范围：在本机从零配置 Go 开发环境，将 Go 源码编译为 **Windows 可执行文件（exe）** 与 **Android 应用包（apk）**。
> 本文档以 **Windows 11** 为例，所有路径、命令均可在新一轮对话中直接照抄执行。
> 配套项目：LANLink（Fyne v2.8 GUI + Gomobile Android 客户端）。

---

## 目录

1. [环境总览](#1-环境总览)
2. [Go 安装与配置](#2-go-安装与配置)
3. [Go Modules 模块管理](#3-go-modules-模块管理)
4. [编译为 Windows 可执行文件（exe）](#4-编译为-windows-可执行文件exe)
5. [Android 编译环境（JDK / SDK / NDK）](#5-android-编译环境jdk--sdk--ndk)
6. [Gomobile 安装与初始化](#6-gomobile-安装与初始化)
7. [Fyne 工具与编译 APK](#7-fyne-工具与编译-apk)
8. [APK 签名配置](#8-apk-签名配置)
9. [一键构建脚本说明](#9-一键构建脚本说明)
10. [常用命令速查表](#10-常用命令速查表)
11. [排错速查](#11-排错速查)

---

<a id="1-环境总览"></a>

## 1. 环境总览

| 组件 | 用途 | 本项目要求 |
|------|------|-----------|
| **Go** | 语言运行时 / 编译器 | ≥ 1.22（建议 1.22.x） |
| **Git** | 版本控制 / 拉取依赖 | ≥ 2.40 |
| **GCC (MinGW-w64)** | CGO 交叉编译 Windows 原生所需的 C 编译器 | 仅 Windows 原生 GUI 需要 |
| **JDK 17** | 编译 / 打包 Android（Fyne 要求 Java 11+） | 本项目隔离用 JDK17，路径 `%USERPROFILE%\tools\jdk-17` |
| **Android SDK** | 平台工具 / build-tools | `%LOCALAPPDATA%\Android\Sdk` |
| **Android NDK** | Go→ARM 原生库编译 | `ndk\25.2.9519653` |
| **Gomobile** | Go 移动端绑定工具链 | `go install golang.org/x/mobile/cmd/gomobile` |
| **Fyne** | GUI 框架 + `fyne package` 打包 APK | `go install fyne.io/fyne/v2/cmd/fyne@latest` |

> LANLink 的 GUI 基于 Fyne，底层用到 OpenGL / CGO，因此 Windows 端需要 MinGW-w64 的 gcc；
> Android 端借助 Gomobile/Fyne 生成 native 库，需要 JDK + SDK + NDK。

---

<a id="2-go-安装与配置"></a>

## 2. Go 安装与配置

### 2.1 下载安装

1. 打开 https://go.dev/dl/ ，下载 **`go1.22.x.windows-amd64.msi`**
2. 双击安装，默认路径 `C:\Program Files\Go\`
3. 安装完成后打开 **PowerShell** 验证：

```powershell
go version
# 期望输出：go version go1.22.x windows/amd64
```

> 若提示 `go : 无法将"go"项识别为...`，说明安装器未写入 PATH。注销重新登录，或手动把
> `C:\Program Files\Go\bin` 加入系统「环境变量 → Path」。

### 2.2 环境变量设置要点

Go 1.16+ 之后绝大多数变量有合理默认值，只需按需设置以下几项：

| 变量 | 默认值 | 说明 / 本项目推荐设置 |
|------|--------|----------------------|
| `GOROOT` | `C:\Program Files\Go` | **不要手动设**，安装器已处理；设错反而报错 |
| `GOPATH` | `%USERPROFILE%\go` | 模块缓存与 `go install` 产物（如 `fyne`、`gomobile`）落在此处的 `bin/` |
| `GOBIN` | `%GOPATH%\bin` | 建议显式加入 PATH：`%USERPROFILE%\go\bin` |
| `GOPROXY` | `https://proxy.golang.org,direct` | **国内必改**（否则拉不动依赖）：`https://goproxy.cn,direct` |
| `GOSUMDB` | `sum.golang.org` | 若代理拉取校验失败可设 `GOSUMDB=off`（不推荐长期关） |
| `GOFLAGS` | 空 | 可加 `-mod=mod` 让 `go build` 自动更新 `go.mod` |

**PowerShell 永久设置（当前用户）：**

```powershell
[Environment]::SetEnvironmentVariable("GOPATH", "$env:USERPROFILE\go", "User")
[Environment]::SetEnvironmentVariable("GOBIN",  "$env:USERPROFILE\go\bin", "User")
[Environment]::SetEnvironmentVariable("GOPROXY", "https://goproxy.cn,direct", "User")
# 把 GOBIN 加入 PATH（用户级）
$old = [Environment]::GetEnvironmentVariable("Path", "User")
if ($old -notlike "*go\bin*") {
    [Environment]::SetEnvironmentVariable("Path", "$old;$env:USERPROFILE\go\bin", "User")
}
```

**验证：**

```powershell
go env GOPATH GOBIN GOPROXY
# 期望 GOPROXY=https://goproxy.cn,direct
```

> ⚠️ 若你的网络访问 GitHub 需要代理（如本项目 push 用 `127.0.0.1:7897`），git 也要配：
> ```powershell
> git config --local http.proxy  http://127.0.0.1:7897
> git config --local https.proxy http://127.0.0.1:7897
> ```

### 2.3 安装 MinGW-w64（Windows 原生 GUI 编译需要）

Fyne 的 Windows 渲染依赖 CGO，需要 gcc（不需要也能编译纯 Go，但 Fyne GUI 必须）：

1. 下载 **MinGW-w64**（推荐 https://www.mingw-w64.org/ 或用 MSYS2）
2. 用 MSYS2 安装更省事：
   ```powershell
   # 假设已装 MSYS2
   pacman -S mingw-w64-x86_64-gcc
   ```
3. 把 `C:\msys64\mingw64\bin` 加入 PATH，验证：
   ```powershell
   gcc --version
   ```

---

<a id="3-go-modules-模块管理"></a>

## 3. Go Modules 模块管理

### 3.1 初始化（新项目）

```powershell
mkdir myproject && cd myproject
go mod init github.com/yourname/myproject
```

### 3.2 本项目已存在的模块结构

LANLink 仓库根目录已有 `go.mod` / `go.sum`，典型内容形如：

```
module github.com/qwerasdzx-123/LANLink

go 1.22

require (
    fyne.io/fyne/v2 v2.8.0
    golang.org/x/mobile v0.0.0-...
    github.com/fyne-io/...
    ...
)
```

### 3.3 日常模块命令

```powershell
go mod tidy      # 清理未用依赖、补齐缺失依赖，并刷新 go.sum（提交前必跑）
go mod download  # 仅下载依赖到本地缓存（CI / 离线构建前）
go mod verify    # 校验依赖完整性
go get -u ./...  # 升级所有直接/间接依赖到最新兼容版本
go get fyne.io/fyne/v2@v2.8.0   # 指定升级某个依赖到某版本
go list -m all   # 列出全部依赖及版本
```

> 📌 改完 `go.mod` 依赖后务必 `go mod tidy` 再编译，否则可能 `missing go.sum entry`。

---

<a id="4-编译为-windows-可执行文件exe"></a>

## 4. 编译为 Windows 可执行文件（exe）

### 4.1 关键编译参数

| 参数 | 作用 | 本项目用法 |
|------|------|-----------|
| `-o <file>` | 指定输出文件名 | `-o LANlink-4.0.exe` |
| `-ldflags "-s -w"` | 去除调试符号与 DWARF，体积更小 | 建议始终加 |
| `-ldflags "-H windowsgui"` | **隐藏黑色控制台窗口**（GUI 程序必需） | Windows GUI 必加 |
| `--tags` | 编译标签 | 本项目无需特殊 tag |
| `GOOS=windows GOARCH=amd64` | 交叉编译目标（本机即 windows/amd64，可省略） | 跨平台时才需要 |

### 4.2 本项目构建命令（GUI 端）

```powershell
cd <仓库根目录>

# 构建 Windows GUI 版（无控制台窗口，体积精简）
go build -ldflags "-H windowsgui -s -w" -o LANlink-4.0.exe ./cmd/lanlink-gui
```

构建成功后根目录出现 `LANlink-4.0.exe`，**双击即可运行，不会再弹出命令行窗口**。

### 4.3 验证 exe 是否为 GUI 子系统（无控制台）

```powershell
# 读取 PE 头 Subsystem 字段：2=GUI(无控制台)，3=Console(有黑窗)
Add-Type -TypeDefinition 'using System; using System.IO; public class PE { public static int Sub(string p){ using(var f=File.OpenRead(p)){ var b=new byte[1024]; f.Read(b,0,b.Length); int e=BitConverter.ToInt32(b,0x3C); return BitConverter.ToUInt16(b,e+0x5C);} } }'
[PE]::Sub("LANlink-4.0.exe")   # 应输出 2
```

### 4.4 交叉编译（举例：在其他平台生成 Windows exe）

```powershell
# 在 Linux/macOS 上生成 Windows 64 位 exe
GOOS=windows GOARCH=amd64 CGO_ENABLED=1 \
  go build -ldflags "-H windowsgui -s -w" -o LANlink-4.0.exe ./cmd/lanlink-gui
# 注意：跨平台编译 Fyne 仍需目标平台的 C 工具链（如 x86_64-w64-mingw32-gcc）
```

---

<a id="5-android-编译环境jdk--sdk--ndk"></a>

## 5. Android 编译环境（JDK / SDK / NDK）

> ⚠️ **隔离原则**：本项目通过 `build-apk.bat` 用 `SETLOCAL/ENDLOCAL` 仅在「当前命令行窗口」临时设置
> JDK17 / Android 环境，**不修改系统全局 `JAVA_HOME` / `PATH`**，因此即便你机器上另有 JDK8 也不会冲突。

### 5.1 安装 JDK 17（隔离路径）

1. 下载 **JDK 17（Temurin / Oracle / Zulu 均可）**，`x64 MSI`
2. 自定义安装路径到：`%USERPROFILE%\tools\jdk-17`
   （即 `C:\Users\<你的用户名>\tools\jdk-17`）
3. **不要**把它写进系统 `JAVA_HOME`，脚本会自动 `SET JAVA_HOME=...`，用完即还原

### 5.2 安装 Android SDK / NDK

建议用 **Android Studio** 的 SDK Manager，或命令行 `cmdline-tools`：

```
安装位置（本项目约定）：
  SDK 根目录 : %LOCALAPPDATA%\Android\Sdk
  NDK       : %LOCALAPPDATA%\Android\Sdk\ndk\25.2.9519653
```

所需组件：
- **Platform-Tools**（含 `adb`）
- **Build-Tools**（如 34.0.0）
- **Platform 34**（android-34）
- **NDK 25.2.9519653**（Gomobile/Fyne 对该版本兼容稳定）

设置（脚本内已包含，手动调试时也可这样设）：

```powershell
$env:JAVA_HOME        = "$env:USERPROFILE\tools\jdk-17"
$env:ANDROID_HOME     = "$env:LOCALAPPDATA\Android\Sdk"
$env:ANDROID_NDK_HOME = "$env:ANDROID_HOME\ndk\25.2.9519653"
$env:PATH            = "$env:ANDROID_HOME\platform-tools;$env:PATH"
```

### 5.3 验证

```powershell
& "$env:JAVA_HOME\bin\java" -version          # 期望 java version "17.x"
& "$env:ANDROID_HOME\platform-tools\adb" version
```

---

<a id="6-gomobile-安装与初始化"></a>

## 6. Gomobile 安装与初始化

### 6.1 安装 gomobile 命令

```powershell
# 需先保证 GOPROXY 已设为国内源（见 2.2）
go install golang.org/x/mobile/cmd/gomobile@latest
go install golang.org/x/mobile/cmd/gobind@latest
# 确认可执行文件在 GOBIN
golang.org\x\mobile\cmd\gomobile  # 路径提示即成功
```

### 6.2 初始化 Android 工具链

首次打包 APK 前必须执行一次（耗时较长，会下载并编译 Android 基础依赖）：

```powershell
# 在「已设置好 JAVA_HOME / ANDROID_HOME / ANDROID_NDK_HOME」的窗口中：
gomobile init
```

> 或在 `fyne package` 时由 Fyne 自动调用 `gomobile` 完成初始化（首次会卡几分钟，属正常）。

---

<a id="7-fyne-工具与编译-apk"></a>

## 7. Fyne 工具与编译 APK

### 7.1 安装 fyne 命令行

```powershell
go install fyne.io/fyne/v2/cmd/fyne@latest
# 验证
fyne version
```

### 7.2 本项目 APK 构建命令

> **重要**：Fyne 移动端构建**不支持 `-src`**，必须 **先 `cd` 进入口目录**再执行 `fyne package`。

```powershell
cd <仓库根目录>\cmd\lanlink-android

fyne package --os android/arm64 --release ^
  -app-id com.kalaspace.lanlink ^
  -name LANLink ^
  -icon Icon.png
```

参数说明：

| 参数 | 含义 |
|------|------|
| `--os android/arm64` | 只构建 64 位 ARM native 库（去掉 armeabi-v7a，体积减半，现代手机均支持） |
| `--release` | 关闭 debug、剥离符号、启用优化，产物更小更快 |
| `-app-id` | Android 包名（`com.kalaspace.lanlink`，需与 `AndroidManifest.xml` 的 `package` 一致） |
| `-name` | 安装后显示的应用名 |
| `-icon` | 应用图标（入口目录下的 `Icon.png`） |

构建产物 `LANLink.apk` 生成在 `cmd\lanlink-android\` 下，`build-apk.bat` 会把它拷贝到仓库根目录。

### 7.3 其他架构（可选）

```powershell
# 同时支持 32/64 位（体积更大）
fyne package --os android --release -app-id com.kalaspace.lanlink -name LANLink -icon Icon.png

# 仅 32 位（老旧设备）
fyne package --os android/arm --release ...
```

### 7.4 AndroidManifest.xml 要点

本项目 `cmd/lanlink-android/AndroidManifest.xml` 已声明必要权限：
- `INTERNET` / `ACCESS_NETWORK_STATE` / `ACCESS_WIFI_STATE`：网络基础
- `CHANGE_WIFI_MULTICAST_STATE`：允许接收 239.x 组播（局域网发现关键）
- `NEARBY_DEVICES`：Android 12+ 局域网设备发现（无需定位权限）

> 注意：`uses-sdk` 与 `android:icon` 由 Fyne 自动合成，**不要**在自定义 manifest 里手写，否则打包冲突。
> 本项目已通过 `multicast_android.go` 用 JNI 申请 `WifiManager.MulticastLock`，提升手机端组播接收成功率。

---

<a id="8-apk-签名配置"></a>

## 8. APK 签名配置

### 8.1 两种签名方式

| 方式 | 说明 | 适用 |
|------|------|------|
| **Debug 签名** | `fyne package` 不加 `--release` 时，Fyne/Gomobile 自动用内置 debug keystore 签名 | 本地调试、模拟器安装 |
| **Release 签名** | 正式发布需用自己的 keystore，否则无法上架 / 覆盖安装 | 发布给用户 |

本项目 `build-apk.bat` 用了 `--release`，但**未指定自定义 keystore**，因此 Fyne 仍会用其 debug key 做 release 构建签名（可安装、可调试，但不具备发布级唯一性）。

### 8.2 生成自己的发布签名（推荐做一次）

```powershell
# 用 JDK 的 keytool 生成 keystore（请牢记别名与密码）
& "$env:JAVA_HOME\bin\keytool" -genkey -v `
  -keystore "$env:USERPROFILE\lanlink-release.keystore" `
  -alias lanlink `
  -keyalg RSA -keysize 2048 -validity 10000
```

### 8.3 用自定义 keystore 给 APK 签名 / 重签

`fyne package`（截至 v2.8）暂不直接接受 keystore 参数，标准做法是：

1. 先用 `fyne package --release` 产出**未正式发布签名**的 APK（实际含 debug 签）
2. 用 `apksigner`（SDK build-tools 自带）对齐 + 重签：

```powershell
$buildTools = "$env:ANDROID_HOME\build-tools\34.0.0"

# 1) 对齐（Android 要求 zipalign 在签名之前）
& "$buildTools\zipalign" -p 4 LANLink.apk LANLink-aligned.apk

# 2) 用你的 keystore 签名
& "$buildTools\apksigner" sign `
  --ks "$env:USERPROFILE\lanlink-release.keystore" `
  --ks-key-alias lanlink `
  --out LANLink-signed.apk `
  LANLink-aligned.apk

# 3) 验证签名
& "$buildTools\apksigner" verify LANLink-signed.apk
```

> 之后把 `LANLink-signed.apk` 安装 / 发布即可。如需让 `build-apk.bat` 自动签名，可在脚本末尾追加上述三步。

---

<a id="9-一键构建脚本说明"></a>

## 9. 一键构建脚本说明

仓库根目录已提供下列脚本，**无需手动拼环境变量**：

| 脚本 | 作用 |
|------|------|
| `build-gui.bat` | 构建 Windows GUI 版（含 `-H windowsgui`） |
| `build-apk.bat` | **隔离**设置 JDK17/SDK/NDK 后 `fyne package` 生成 `LANLink.apk` |
| `build-and-debug.bat` | 构建 APK 后调用 `debug-apk.bat` 自动装进 MuMu 模拟器运行 |
| `debug-apk.bat` / `debug-apk.ps1` | 通过 MuMu CLI 安装并启动 APK |

**直接双击 `build-apk.bat` 即可**，它内部已：
- `SETLOCAL` 隔离 JDK17 / Android 环境（不影响系统其它 JDK）
- 设置 `GOPROXY=https://goproxy.cn,direct`
- 把 `%GOPATH%\bin` 加入 PATH（让 `fyne` 可用）
- `cd cmd\lanlink-android` 后执行 `fyne package --os android/arm64 --release ...`
- 把产物拷贝到仓库根目录 `LANLink.apk`

> 首次运行会自动下载 gomobile 并编译 Android 工具链，耐心等待数分钟到十几分钟。

---

<a id="10-常用命令速查表"></a>

## 10. 常用命令速查表

### Go 基础
```powershell
go version                  # 查看版本
go env GOPATH GOBIN GOPROXY # 查看关键变量
go mod init <module>        # 初始化模块
go mod tidy                 # 整理依赖（提交前必跑）
go mod download             # 下载依赖
go get <pkg>@<ver>          # 增加/升级依赖
go build ./...              # 编译全部包（不产出文件）
go build -o app.exe ./cmd/x # 编译指定入口为 exe
go vet ./...                # 静态检查
go fmt ./...                # 格式化
go run ./cmd/lanlink-gui    # 直接运行（不产文件）
```

### Windows exe 构建（本项目）
```powershell
go build -ldflags "-H windowsgui -s -w" -o LANlink-4.0.exe ./cmd/lanlink-gui
```

### Android APK 构建（本项目）
```powershell
# 方式一：一键（推荐）
build-apk.bat

# 方式二：手动（需先配 JDK/SDK/NDK 环境变量）
cd cmd\lanlink-android
fyne package --os android/arm64 --release -app-id com.kalaspace.lanlink -name LANLink -icon Icon.png
```

### Gomobile / Fyne 工具
```powershell
go install golang.org/x/mobile/cmd/gomobile@latest
go install fyne.io/fyne/v2/cmd/fyne@latest
gomobile init              # 初始化 Android 工具链（首次）
fyne version               # 验证 fyne 命令
```

### Git（本项目走代理示例）
```powershell
git config --local http.proxy  http://127.0.0.1:7897
git config --local https.proxy http://127.0.0.1:7897
git add <files>            # 暂存（本项目排除 bat/ps/exe/apk）
git commit -m "msg"
git push origin main
git fetch origin           # 刷新远程引用
git rev-list --left-right --count origin/main...HEAD  # 查看领先/落后
```

### ADB（模拟器 / 真机调试）
```powershell
adb devices               # 列出已连接设备
adb install LANLink.apk   # 安装
adb uninstall com.kalaspace.lanlink
adb logcat                # 查看运行日志（排错）
```

---

<a id="11-排错速查"></a>

## 11. 排错速查

| 现象 | 可能原因 | 解决 |
|------|----------|------|
| `go: command not found` | PATH 未含 Go bin | 把 `C:\Program Files\Go\bin` 加入 PATH 并重启终端 |
| 拉依赖卡住 / `connection refused` | GOPROXY 不可达 | 设 `GOPROXY=https://goproxy.cn,direct` |
| `missing go.sum entry` | 依赖未对齐 | `go mod tidy` |
| 构建 exe 弹出黑窗口 | 没加 `-H windowsgui` | 加 `-ldflags "-H windowsgui"` |
| `gcc: command not found` | 缺 MinGW-w64 | 装 MinGW-w64 并把 `mingw64\bin` 加入 PATH |
| `fyne: command not found` | 未 `go install fyne` 或 GOBIN 不在 PATH | `go install fyne.io/fyne/v2/cmd/fyne@latest` 并把 `%GOPATH%\bin` 加入 PATH |
| APK 打包报 JDK 版本错 | 用了 JDK8 等旧版 | 用脚本隔离的 JDK17，或手动 `SET JAVA_HOME=...\jdk-17` |
| `NDK not found` | ANDROID_NDK_HOME 错 | 指向 `Sdk\ndk\25.2.9519653` |
| 首次打包极慢 | 正在下载 gomobile 工具链 | 正常现象，等待数分钟 |
| 手机收不到发现报文 | 组播被 Android 丢弃 | 本项目已用 `MulticastLock`；确认 `CHANGE_WIFI_MULTICAST_STATE` 权限已声明 |
| `git push` 连不上 github:443 | 网络需代理 | `git config --local https.proxy http://127.0.0.1:7897` |
| 装 APK 提示「签名冲突」 | debug/release key 不一致 | 卸载旧版再装，或统一用同一 keystore 重签 |

---

> 📌 **新一轮对话可直接参照本文件第 4、7、9、10 节执行**，无需重新摸索环境。
> 如遇版本号变动（如 Fyne / NDK 升级），以 `go.mod` 与 `build-apk.bat` 中的实际版本为准。
