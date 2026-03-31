package main

import (
	"image/color"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
)

const (
	splashHold    = 1600 * time.Millisecond
	splashFadeOut = 450 * time.Millisecond
)

// Matches gradient “floor” (#062248) — overlay fades to this before closing.
var fadeOverlayRGB = struct{ R, G, B uint8 }{R: 0x06, G: 0x22, B: 0x48}

// showStartupSplash shows a short borderless splash with the tray logo on a gradient
// (desktop drivers only). Safe to call on every launch.
func showStartupSplash(a fyne.App) {
	drv, ok := a.Driver().(desktop.Driver)
	if !ok {
		return
	}

	w := drv.CreateSplashWindow()
	w.SetFixedSize(true)
	w.Resize(fyne.NewSize(440, 300))

	// Deep navy → blue accent, aligned with modernTheme primary (#0a84ff).
	top := color.NRGBA{R: 0x0c, G: 0x10, B: 0x1a, A: 0xff}
	bottom := color.NRGBA{R: 0x06, G: 0x22, B: 0x48, A: 0xff}
	grad := canvas.NewLinearGradient(top, bottom, 125)

	logo := canvas.NewImageFromResource(resourceLogoPng)
	logo.FillMode = canvas.ImageFillContain
	logo.SetMinSize(fyne.NewSize(112, 112))

	title := canvas.NewText("NeoFS", color.NRGBA{R: 0xf2, G: 0xf4, B: 0xf8, A: 0xff})
	title.TextSize = 22
	title.TextStyle = fyne.TextStyle{Bold: true}

	tag := canvas.NewText("Decentralized storage in File Explorer", color.NRGBA{R: 0x8b, G: 0x95, B: 0xa8, A: 0xff})
	tag.TextSize = 12

	textCol := container.NewVBox(
		layout.NewSpacer(),
		container.NewCenter(logo),
		layout.NewSpacer(),
		container.NewCenter(title),
		container.NewCenter(tag),
		layout.NewSpacer(),
	)
	padded := container.NewPadded(textCol)

	fadeOverlay := canvas.NewRectangle(color.Transparent)
	fadeOverlay.FillColor = color.NRGBA{R: fadeOverlayRGB.R, G: fadeOverlayRGB.G, B: fadeOverlayRGB.B, A: 0}

	w.SetContent(container.NewStack(grad, padded, fadeOverlay))
	w.Show()

	var closeOnce sync.Once
	closeSplash := func() {
		closeOnce.Do(func() {
			fyne.Do(w.Close)
		})
	}

	time.AfterFunc(splashHold, func() {
		fyne.Do(func() {
			anim := fyne.NewAnimation(splashFadeOut, func(p float32) {
				fadeOverlay.FillColor = color.NRGBA{
					R: fadeOverlayRGB.R, G: fadeOverlayRGB.G, B: fadeOverlayRGB.B,
					A: uint8(255 * p),
				}
				canvas.Refresh(fadeOverlay)
				if p >= 1 {
					closeSplash()
				}
			})
			anim.Curve = fyne.AnimationEaseOut
			anim.Start()
			time.AfterFunc(splashFadeOut+100*time.Millisecond, closeSplash)
		})
	})
}
