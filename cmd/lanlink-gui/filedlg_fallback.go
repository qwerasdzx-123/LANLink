//go:build !windows

package main

// non‑windows 平台回退到 Fyne 文件对话框。
func nativeFileOpen(title string) (string, error) {
	return "", nil
}
