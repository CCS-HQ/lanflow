// 澜流 Lanflow: a Mihomo desktop client with MyGo native UI.
package main

import (
	"errors"
	"io"
	"log"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/egoist/mygo"
	_ "github.com/egoist/mygo/locales/zh-Hans" // MyGo's own strings in Chinese
	"github.com/egoist/mygo/ui"

	"lanflow/internal/app"
	"lanflow/internal/app/fake"
	"lanflow/internal/i18n"
	"lanflow/internal/split"
	"lanflow/internal/view"
	"lanflow/internal/view/env"
)

// backend picks the service: the in-memory fake only with LANFLOW_FAKE=1
// (development), the real service otherwise (main_service.go). lang is the
// language of a first run.
//
// A real service that cannot be created ends the process with a native
// error dialog. It never falls back to the fake: that would show sample
// nodes and traffic, switches that seem to work while nothing is proxied,
// and serve the same made-up data to ctl and MCP clients.
func backend(lang string) app.API {
	if os.Getenv("LANFLOW_FAKE") == "1" {
		return fake.New()
	}
	api, err := newService(lang)
	if err != nil {
		backendFailed(lang, err)
	}
	return api
}

// backendFailed reports why Lanflow cannot start (log, stderr and a
// dialog) and exits with status 1. tellUser and exit are variables for
// tests.
func backendFailed(lang string, err error) {
	log.Printf("lanflow: cannot start the service: %v", err)
	i18n.SetLang(lang)
	tellUser(startFailureText(err))
	exit(1)
}

var (
	tellUser = showError // main_split.go: a message box without a window
	exit     = os.Exit
)

// startFailureText is the dialog's text for err.
func startFailureText(err error) string {
	return i18n.Tf("澜流无法启动：%s\n\n请检查数据目录是否可以读写；问题仍然存在时，请带上这段信息反馈。", err.Error())
}

func main() {
	if onPhone {
		runPhone() // main_phone.go: one process, no subcommands, tray or local API
		return
	}
	if code, ok := runSubcommand(os.Args[1:]); ok {
		os.Exit(code)
	}
	mygo.App.SetName("Lanflow")
	// The app is written in Chinese and English (docs/design.md §5.8 语言);
	// a first run follows the system's language, afterwards the setting
	// rules, for the app's own texts (i18n) and MyGo's (SetLocale).
	mygo.App.SetLanguages("zh-Hans", "en")
	// Two processes (docs/service-split.md S2): `Lanflow --service` holds
	// the service, the tray and the shortcuts; a plain launch is the
	// window, a client of the service. LANFLOW_SINGLE=1 (or the marker
	// file <DataDir>/single-process) keeps the single process.
	args := split.ParseArgs(os.Args[1:])
	if args.WaitExit > 0 {
		split.WaitExit(args.WaitExit, 30*time.Second)
	}
	dataDir := app.DefaultOptions("Lanflow").DataDir
	if maybeElevate(args, dataDir) {
		os.Exit(0)
	}
	switch {
	case split.SingleProcess(dataDir):
		runSingle(false)
	case args.Service:
		runService(args)
	default:
		runWindow(args)
	}
}

// runSingle is Lanflow in one process, as before the split: the service,
// the window, the tray and the shortcuts together. locked is true when the
// caller (runWindow fallback) already acquired the single-instance lock.
func runSingle(locked bool) {
	if !locked && !mygo.App.RequestSingleInstanceLock() {
		return // the running instance takes over
	}
	mygo.App.SetActivationPolicy(mygo.ActivationPolicyRegular)
	api := backend(i18n.FromSystem(mygo.App.PreferredLanguages()))
	// A panic on the main goroutine (UI callbacks run here) is recorded in
	// errors.jsonl, then raised again (internal/app/errlog.go).
	if c, ok := api.(interface{ CatchPanic(where string) }); ok {
		defer c.CatchPanic("main")
	}
	// Lanflow's own log.Printf lines also go to lanflow.log (internal/applog).
	if w, ok := api.(interface{ AppLogWriter() io.Writer }); ok {
		log.SetOutput(io.MultiWriter(os.Stderr, w.AppLogWriter()))
	}
	i18n.SetLang(api.Settings().Language)
	view.ApplyLanguage(api.Settings().Language)
	view.ApplyAppearance(api.Settings().Appearance)
	shell := view.New(api, view.Options{
		Mac:      runtime.GOOS == "darwin",
		Windows:  runtime.GOOS == "windows",
		Elevated: isElevated(),
	})
	stopLocalAPI := startLocalAPI(api, agentPrompt(shell), singleHost{shell})
	if p := os.Getenv("LANFLOW_PAGE"); p != "" && os.Getenv("LANFLOW_FAKE") == "1" {
		// Development aid: open on a page, e.g. "subs" or "subs:import-url".
		page, arg, _ := strings.Cut(p, ":")
		shell.Env().Navigate(env.Page(page), arg)
	}

	var win *mygo.Window
	var offAutostart func()
	var relaunchElevating atomic.Bool
	var signaled atomic.Bool
	quitting := false
	shell.SetRelaunchElevated(func() {
		if !relaunchElevating.CompareAndSwap(false, true) {
			return
		}
		go func() {
			err := split.RunElevated(executable(), split.WaitExitFlag+strconv.Itoa(os.Getpid()), split.NoElevateFlag)
			if err != nil {
				relaunchElevating.Store(false)
				if !errors.Is(err, split.ErrElevationCanceled) {
					shell.Env().Toast(err.Error(), true)
				}
				return
			}
			signaled.Store(true)
			mygo.App.Quit()
		}()
	})
	showWindow := func() {
		if win != nil {
			shell.ShowWindow()
		}
	}

	// The tray keeps the app alive once the window is hidden.
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.OnSecondInstance(func([]string, string) { showWindow() })
	mygo.App.OnActivate(func(hasVisibleWindows bool) {
		if !hasVisibleWindows {
			showWindow()
		}
	})
	// Deep links (lanflow://import?url=…, clash://install-config?url=…) open
	// the import dialog with the link filled in; registered before Run so
	// the URL the app was launched with arrives too (docs/design.md §9 / §6.1).
	mygo.App.OnOpenURL(shell.OpenImportLink)
	// Wake and connectivity changes from the OS; the service also detects
	// them itself (internal/app/watch.go) where these events are missing.
	if w, ok := api.(interface {
		Resumed()
		NetworkChanged()
	}); ok {
		mygo.Power.OnResume(w.Resumed)
		mygo.Network.OnChanged(func(mygo.NetworkStatus) { w.NetworkChanged() })
	}
	// A termination signal (pkill, logout scripts, an installer replacing
	// the app) must not wait on the quit confirmation nobody will answer:
	// quit straight away; OnWillQuit still restores the system proxy and DNS.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigs
		signaled.Store(true)
		mygo.App.Quit()
	}()
	mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
		if quitting || signaled.Load() {
			quitting = true
			return
		}
		// The confirmation (docs/design.md §6.4) also applies to quitting
		// from the tray while the window is hidden: bring it up and ask.
		if win != nil && shell.NeedsQuitConfirm() {
			e.PreventDefault()
			shell.AskQuit(func() {
				quitting = true
				mygo.App.Quit()
			})
			showWindow()
			return
		}
		quitting = true
	})
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) {
		if offAutostart != nil {
			offAutostart()
		}
		stopLocalAPI()
		shell.Close()
		api.Shutdown()
	})

	mygo.App.WhenReady(func() {
		settings := api.Settings()
		exe := executable()
		queueAutostart(exe, settings.OpenAtLogin, settings.RunAsAdmin)
		offAutostart = api.OnChange(func(t app.Topic) {
			if t == app.TopicSettings {
				set := api.Settings()
				queueAutostart(exe, set.OpenAtLogin, set.RunAsAdmin)
			}
		})
		win = newMainWindow(shell, settings.SilentStart)
		win.OnClose(func(e *mygo.CloseEvent) {
			if quitting {
				return
			}
			if api.Settings().CloseToTray {
				e.PreventDefault()
				win.Hide()
				shell.HiddenToTray()
				return
			}
			mygo.App.Quit()
		})
		if err := shell.StartTray(); err != nil {
			log.Printf("lanflow: tray: %v", err)
		}
		if err := shell.RegisterGlobalShortcut(); err != nil {
			log.Printf("lanflow: global shortcut: %v", err)
		}
		for action, err := range shell.RegisterShortcuts() { // the other actions (view/shortcuts.go)
			log.Printf("lanflow: shortcut %s: %v", action, err)
		}
		shell.CheckUpdatesAtLaunch()
		registerURLScheme()
		dropLegacyInstallEntry()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// newMainWindow creates the main window over shell.
func newMainWindow(shell *view.Shell, hidden bool) *mygo.Window {
	// The default size fits the pages without page-level scrolling
	// (docs/design.md §4); on a first launch it is clamped to the
	// primary display's work area. StateKey restores the size and
	// position of the last run over these.
	width, height := view.FitWorkArea(mygo.Screen.PrimaryDisplay().WorkArea)
	opts := mygo.WindowOptions{
		Title:     i18n.T("澜流 Lanflow"),
		Width:     width,
		Height:    height,
		MinWidth:  view.MinWidth,
		MinHeight: view.MinHeight,
		StateKey:  "main",
		Hidden:    hidden,
		Content:   ui.View(shell.View),
	}
	if runtime.GOOS == "darwin" {
		opts.TitleBarStyle = mygo.TitleBarHiddenInset
		opts.Vibrancy = mygo.VibrancySidebar
	}
	win := mygo.NewWindow(opts)
	shell.Attach(win)
	return win
}

// registerURLScheme registers lanflow:// and clash:// on Windows and Linux, where
// portable builds have no installer to do it.
func registerURLScheme() {
	if runtime.GOOS == "darwin" {
		return
	}
	for _, scheme := range []string{"lanflow", "clash"} {
		if err := mygo.App.RegisterURLScheme(scheme); err != nil {
			log.Printf("lanflow: url scheme %s: %v", scheme, err)
		}
	}
}

// setupLogging sends Lanflow's own log.Printf lines also to lanflow.log
// (internal/applog) when api keeps it.
func setupLogging(api app.API) {
	if w, ok := api.(interface{ AppLogWriter() io.Writer }); ok {
		log.SetOutput(io.MultiWriter(os.Stderr, w.AppLogWriter()))
	}
}
