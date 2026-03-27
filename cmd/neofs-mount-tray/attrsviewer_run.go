package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

func runNeoFSAttrsViewer(targetPath string) int {
	text, ok := buildNeoFSAttrsReport(targetPath)

	a := app.NewWithID("org.neofs.mount.attrs")
	a.SetIcon(resourceLogoPng)
	a.Settings().SetTheme(&modernTheme{})

	title := "NeoFS object details"
	if !ok {
		title = "NeoFS object details — error"
	}
	w := a.NewWindow(title)
	w.SetIcon(resourceLogoPng)
	w.Resize(fyne.NewSize(580, 540))
	w.SetFixedSize(true)

	body := widget.NewLabel(text)
	body.Wrapping = fyne.TextWrapWord
	body.TextStyle = fyne.TextStyle{Monospace: true}
	scroll := container.NewVScroll(body)
	scroll.SetMinSize(fyne.NewSize(540, 420))

	closeBtn := widget.NewButton("Close", func() { w.Close() })
	w.SetContent(container.NewBorder(nil, container.NewPadded(closeBtn), nil, nil, container.NewPadded(scroll)))
	w.SetCloseIntercept(func() { w.Close() })
	w.SetOnClosed(func() { a.Quit() })

	w.Show()
	a.Run()
	if !ok {
		return 1
	}
	return 0
}
