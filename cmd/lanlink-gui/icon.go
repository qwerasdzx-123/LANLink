package main

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

//go:embed assets/icon.png
var iconBytes []byte

// appIcon 程序图标（窗口 + 系统托盘共用）
var appIcon = fyne.NewStaticResource("lanlink.png", iconBytes)
