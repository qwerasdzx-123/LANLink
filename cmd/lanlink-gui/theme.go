package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

// macTheme macOS 风格主题：SF 蓝为主色，大圆角，支持明亮/暗黑两套配色。
type macTheme struct {
	dark bool
}

func (t macTheme) variant() fyne.ThemeVariant {
	if t.dark {
		return theme.VariantDark
	}
	return theme.VariantLight
}

func (t macTheme) Color(n fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	v := t.variant()
	switch n {
	case theme.ColorNamePrimary:
		return color.NRGBA{0x0A, 0x84, 0xFF, 0xFF} // macOS 系统蓝
	case theme.ColorNameFocus:
		return color.NRGBA{0x0A, 0x84, 0xFF, 0x66}
	case theme.ColorNameBackground:
		if t.dark {
			return color.NRGBA{0x1C, 0x1C, 0x1E, 0xFF}
		}
		return color.NRGBA{0xF5, 0xF5, 0xF7, 0xFF}
	case theme.ColorNameInputBackground:
		if t.dark {
			return color.NRGBA{0x2C, 0x2C, 0x2E, 0xFF}
		}
		return color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	case theme.ColorNameButton:
		if t.dark {
			return color.NRGBA{0x3A, 0x3A, 0x3C, 0xFF}
		}
		return color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	case theme.ColorNameSeparator:
		if t.dark {
			return color.NRGBA{0x48, 0x48, 0x4A, 0xFF}
		}
		return color.NRGBA{0xD1, 0xD1, 0xD6, 0xFF}
	case theme.ColorNameHover:
		if t.dark {
			return color.NRGBA{0xFF, 0xFF, 0xFF, 0x14}
		}
		return color.NRGBA{0x00, 0x00, 0x00, 0x0D}
	case theme.ColorNameSelection:
		return color.NRGBA{0x0A, 0x84, 0xFF, 0x44}
	}
	return theme.DefaultTheme().Color(n, v)
}

func (t macTheme) Font(s fyne.TextStyle) fyne.Resource { return theme.DefaultTheme().Font(s) }
func (t macTheme) Icon(n fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(n)
}

func (t macTheme) Size(n fyne.ThemeSizeName) float32 {
	switch n {
	case theme.SizeNameInputRadius:
		return 8 // mac 式大圆角输入框
	case theme.SizeNameSelectionRadius:
		return 6
	}
	return theme.DefaultTheme().Size(n)
}

// 聊天气泡配色（随主题切换）
func bubbleColor(dark, mine bool) color.Color {
	if dark {
		if mine {
			return color.NRGBA{0x0B, 0x5F, 0xB4, 0xFF} // 深蓝
		}
		return color.NRGBA{0x2C, 0x2C, 0x2E, 0xFF} // 深灰
	}
	if mine {
		return color.NRGBA{0xD1, 0xE8, 0xFF, 0xFF} // iMessage 浅蓝
	}
	return color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF} // 白
}

// metaColor 气泡上方发送者/时间小字颜色
func metaColor(dark bool) color.Color {
	if dark {
		return color.NRGBA{0x8E, 0x8E, 0x93, 0xFF}
	}
	return color.NRGBA{0x99, 0x99, 0x99, 0xFF}
}

// 聊天区背景色
func chatBgColor(dark bool) color.Color {
	if dark {
		return color.NRGBA{0x1C, 0x1C, 0x1E, 0xFF}
	}
	return color.NRGBA{0xF2, 0xF2, 0xF7, 0xFF}
}
