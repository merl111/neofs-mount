package main

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
)

type modernTheme struct{}

var _ fyne.Theme = (*modernTheme)(nil)

func (m modernTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	// Always use dark variant for a modern, sleek look
	switch name {
	case theme.ColorNameBackground:
		return color.NRGBA{R: 0x12, G: 0x12, B: 0x14, A: 0xff} // Very dark grey/black
	case theme.ColorNameButton:
		return color.NRGBA{R: 0x1e, G: 0x1e, B: 0x24, A: 0xff}
	case theme.ColorNameDisabledButton:
		return color.NRGBA{R: 0x2a, G: 0x2a, B: 0x30, A: 0xff}
	case theme.ColorNameDisabled:
		return color.NRGBA{R: 0x60, G: 0x60, B: 0x66, A: 0xff}
	case theme.ColorNameHover:
		return color.NRGBA{R: 0x2a, G: 0x2a, B: 0x30, A: 0xff}
	case theme.ColorNameFocus:
		return color.NRGBA{R: 0x00, G: 0xe6, B: 0x76, A: 0x40} // Neo green with alpha
	case theme.ColorNameSelection:
		return color.NRGBA{R: 0x00, G: 0xe6, B: 0x76, A: 0x40}
	case theme.ColorNamePrimary:
		return color.NRGBA{R: 0x00, G: 0xe6, B: 0x76, A: 0xff} // Neo green accent
	case theme.ColorNameForeground:
		return color.NRGBA{R: 0xf5, G: 0xf5, B: 0xf5, A: 0xff} // Off-white text
	case theme.ColorNameMenuBackground:
		return color.NRGBA{R: 0x1e, G: 0x1e, B: 0x24, A: 0xff}
	case theme.ColorNameSeparator:
		return color.NRGBA{R: 0x2a, G: 0x2a, B: 0x30, A: 0xff}
	case theme.ColorNameError:
		return color.NRGBA{R: 0xf4, G: 0x43, B: 0x36, A: 0xff}
	case theme.ColorNameSuccess:
		return color.NRGBA{R: 0x00, G: 0xe6, B: 0x76, A: 0xff} // Neo green
	case theme.ColorNameScrollBar:
		return color.NRGBA{R: 0x33, G: 0x33, B: 0x38, A: 0x99}
	case theme.ColorNameInputBackground:
		return color.NRGBA{R: 0x1a, G: 0x1a, B: 0x1f, A: 0xff}
	case theme.ColorNamePlaceHolder:
		return color.NRGBA{R: 0x66, G: 0x66, B: 0x70, A: 0xff}
	}
	return theme.DefaultTheme().Color(name, theme.VariantDark)
}

func (m modernTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}

func (m modernTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

func (m modernTheme) Size(name fyne.ThemeSizeName) float32 {
	switch name {
	case theme.SizeNamePadding:
		return 8
	case theme.SizeNameInnerPadding:
		return 8
	case theme.SizeNameText:
		return 14
	case theme.SizeNameHeadingText:
		return 24
	case theme.SizeNameSubHeadingText:
		return 18
	case theme.SizeNameInlineIcon:
		return 20
	case theme.SizeNameScrollBar:
		return 12
	case theme.SizeNameScrollBarSmall:
		return 4
	case theme.SizeNameSeparatorThickness:
		return 1
	}
	return theme.DefaultTheme().Size(name)
}
