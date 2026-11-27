package main

import (
	"log"
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"lanflow/internal/i18n"
	"lanflow/internal/view"
)

// onPhone is true on iOS and Android, where Lanflow is one process: no
// service split (iOS starts no programs), no single-instance lock, tray,
// global shortcuts, login item, updater or local API for ctl and MCP. The
// core runs in the system VPN (internal/platform/tunnel).
const onPhone = runtime.GOOS == "ios" || runtime.GOOS == "android"

// runPhone is main on a phone.
func runPhone() {
	mygo.App.SetName("Lanflow")
	mygo.App.SetLanguages("zh-Hans", "en")
	api := backend(i18n.FromSystem(mygo.App.PreferredLanguages()))
	if c, ok := api.(interface{ CatchPanic(where string) }); ok {
		defer c.CatchPanic("main")
	}
	setupLogging(api)
	i18n.SetLang(api.Settings().Language)
	view.ApplyLanguage(api.Settings().Language)
	view.ApplyAppearance(api.Settings().Appearance)
	shell := view.New(api, view.Options{Phone: true})

	// Deep links (lanflow://, clash://install-config) open the import
	// dialog, as on the desktop; registered before Run for the launch URL.
	mygo.App.OnOpenURL(shell.OpenImportLink)
	// The system may end a phone app in the background at any time and
	// never asks it to quit: frames stop on suspend, so slow the service's
	// streams down as a hidden desktop window does. The VPN keeps running
	// apart from the app.
	if b, ok := api.(interface{ SetBackground(bool) }); ok {
		mygo.Power.OnSuspend(func() { b.SetBackground(true) })
		mygo.Power.OnResume(func() { b.SetBackground(false) })
	}
	if w, ok := api.(interface {
		Resumed()
		NetworkChanged()
	}); ok {
		mygo.Power.OnResume(w.Resumed)
		mygo.Network.OnChanged(func(mygo.NetworkStatus) { w.NetworkChanged() })
	}
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:   i18n.T("澜流 Lanflow"),
			Content: ui.View(shell.View),
		})
		shell.Attach(win)
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}
