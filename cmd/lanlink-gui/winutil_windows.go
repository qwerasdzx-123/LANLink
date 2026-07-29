//go:build windows

package main

import (
	"errors"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows/registry"
)

var (
	user32            = syscall.NewLazyDLL("user32.dll")
	procFindWindowW   = user32.NewProc("FindWindowW")
	procFlashWindowEx = user32.NewProc("FlashWindowEx")
	procMessageBeep   = user32.NewProc("MessageBeep")
)

type flashWInfo struct {
	CbSize    uint32
	Hwnd      uintptr
	DwFlags   uint32
	UCount    uint32
	DwTimeout uint32
}

const (
	flashwAll       = 0x3 // 标题栏 + 任务栏一起闪
	flashwTimerNoFG = 0xC // 持续闪烁直到窗口取得焦点
)

// flashTaskbar 任务栏闪烁提示（按窗口标题查找）
func flashTaskbar(title string) {
	p, err := syscall.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(p)))
	if hwnd == 0 {
		return
	}
	fi := flashWInfo{
		CbSize:  uint32(unsafe.Sizeof(flashWInfo{})),
		Hwnd:    hwnd,
		DwFlags: flashwAll | flashwTimerNoFG,
		UCount:  8,
	}
	procFlashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
}

// playBeep 播放系统提示音
func playBeep() {
	procMessageBeep.Call(0xFFFFFFFF) // MB_OK 默认提示音
}

const autoRunKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// setAutoStart 写入/移除 HKCU Run 注册表键实现开机自启
func setAutoStart(enable bool) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, autoRunKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if enable {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		return k.SetStringValue("LanLink", `"`+exe+`"`)
	}
	if err := k.DeleteValue("LanLink"); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return err
	}
	return nil
}
