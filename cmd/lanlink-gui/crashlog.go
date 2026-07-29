package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"fyne.io/fyne/v2/dialog"
	"golang.org/x/sys/windows"
)

// crashLogPath 崩溃日志路径（用户临时目录），用于无控制台下保留 panic 堆栈。
func crashLogPath() string {
	return filepath.Join(os.TempDir(), "lanlink-gui-crash.log")
}

func appendCrashLog(s string) {
	f, err := os.OpenFile(crashLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(s)
}

// initCrashLog 将进程标准错误重定向到日志文件（含底层句柄），
// 使 windowsgui 子系统程序崩溃时也能保留 panic 堆栈供诊断。
func initCrashLog() {
	f, err := os.OpenFile(crashLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	os.Stderr = f
	_ = windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd()))
}

// recoverUI 捕获 UI 回调中的 panic，避免无控制台下整个进程闪退，并记录堆栈。
func (g *gui) recoverUI(where string) {
	if r := recover(); r != nil {
		buf := make([]byte, 1<<16)
		n := runtime.Stack(buf, false)
		line := fmt.Sprintf("[%s] panic in %s: %v\n%s\n", time.Now().Format("2006-01-02 15:04:05"), where, r, buf[:n])
		appendCrashLog(line)
		if g != nil && g.win != nil {
			dialog.ShowError(fmt.Errorf("操作失败(%s)：%v", where, r), g.win)
		}
	}
}
