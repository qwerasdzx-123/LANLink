//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// 精简版 OPENFILENAMEW，结构定义参考 MSDN
// https://learn.microsoft.com/en-us/windows/win32/api/commdlg/ns-commdlg-openfilenamew
type openfilenameW struct {
	lStructSize       uint32
	hwndOwner         uintptr
	hInstance         uintptr
	lpstrFilter       *uint16
	lpstrCustomFilter *uint16
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         *uint16
	nMaxFile          uint32
	lpstrFileTitle    *uint16
	nMaxFileTitle     uint32
	lpstrInitialDir   *uint16
	lpstrTitle        *uint16
	Flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
}

const (
	OFN_EXPLORER      = 0x00080000
	OFN_HIDEREADONLY  = 0x00000004
	OFN_FILEMUSTEXIST = 0x00001000
	OFN_PATHMUSTEXIST = 0x00000800
)

var (
	comdlg32             = windows.MustLoadDLL("comdlg32.dll")
	procGetOpenFileNameW = comdlg32.MustFindProc("GetOpenFileNameW")
)

// buildFilter 构造双空终止的过滤器 UTF16 数组（格式: 描述\0通配\0\0）
func buildFilter() []uint16 {
	d, _ := windows.UTF16FromString("所有文件 (*.*)")
	p, _ := windows.UTF16FromString("*.*")
	out := make([]uint16, len(d)+len(p)+1) // 最后多一个 0 作为结束空串
	copy(out, d)
	copy(out[len(d):], p)
	return out
}

// nativeFileOpen 弹出 Windows 原生文件选择对话框（通过 GetOpenFileNameW）。
// 毫秒级响应，无 CMD 窗口。
func nativeFileOpen(title string) (string, error) {
	buf := make([]uint16, 32768)
	if title == "" {
		title = "选择要发送的文件"
	}

	filter := buildFilter()
	titlePtr, _ := windows.UTF16PtrFromString(title)

	ofn := openfilenameW{
		lStructSize: uint32(unsafe.Sizeof(openfilenameW{})),
		lpstrFilter: &filter[0],
		lpstrFile:   &buf[0],
		nMaxFile:    uint32(len(buf)),
		lpstrTitle:  titlePtr,
		Flags:       OFN_EXPLORER | OFN_HIDEREADONLY | OFN_FILEMUSTEXIST | OFN_PATHMUSTEXIST,
	}

	ret, _, _ := procGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if ret == 0 {
		return "", nil // 用户取消
	}
	return windows.UTF16PtrToString(&buf[0]), nil
}
