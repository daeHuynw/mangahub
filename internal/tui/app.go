// Package tui is the MangaHub terminal client built with tview.
//
// Threading rule (assignment §4): tview widgets may only be touched by the
// tview event loop. Network calls run in goroutines started by runAsync, and
// their results come back through app.QueueUpdateDraw, which executes the
// callback inside the event loop and redraws. No other code path may modify a
// widget from a network goroutine.
package tui

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"mangahub/internal/tui/client"
)

// Page names.
const (
	pageLogin   = "login"
	pageBrowser = "browser"
	pageLibrary = "library"
	pageUpdate  = "update" // Update Chapter dialog, shown over the library
	pageHelp    = "help"
)

// App is the whole TUI: one tview.Application, one HTTP client, the screens.
type App struct {
	cfg Config
	tv  *tview.Application
	api *client.Client

	pages  *tview.Pages
	header *tview.TextView
	status *tview.TextView

	login   *loginView
	browser *browserView
	library *libraryView
	help    *helpView

	loggedIn bool // only read/written in the event loop
}

// Option customises an App (used by tests to inject a simulation screen).
type Option func(*App)

// WithScreen makes the app draw on s instead of the real terminal.
func WithScreen(s tcell.Screen) Option { return func(a *App) { a.tv.SetScreen(s) } }

// New builds every screen; nothing touches the network until the user acts.
func New(cfg Config, opts ...Option) *App {
	a := &App{
		cfg:    cfg,
		tv:     tview.NewApplication(),
		api:    client.New(cfg.HTTP),
		pages:  tview.NewPages(),
		header: tview.NewTextView().SetDynamicColors(true),
		status: tview.NewTextView().SetDynamicColors(true),
	}
	a.login = newLoginView(a)
	a.browser = newBrowserView(a)
	a.library = newLibraryView(a)
	a.help = newHelpView(a)

	a.pages.AddPage(pageLogin, a.login.root, true, true)
	a.pages.AddPage(pageBrowser, a.browser.root, true, false)
	a.pages.AddPage(pageLibrary, a.library.root, true, false)
	a.pages.AddPage(pageHelp, a.help.root, true, false)

	root := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.header, 1, 0, false).
		AddItem(a.pages, 0, 1, true).
		AddItem(a.status, 1, 0, false)
	a.tv.SetRoot(root, true).SetInputCapture(a.globalKeys)
	a.refreshHeader()
	a.setStatus(statusInfo, "Log in or register. Tab moves between fields, Enter confirms, Ctrl+C quits.")

	for _, o := range opts {
		o(a)
	}
	return a
}

// Run blocks until the user quits (Ctrl+C).
func (a *App) Run() error { return a.tv.Run() }

// Stop ends Run; safe to call from any goroutine.
func (a *App) Stop() { a.tv.Stop() }

// runAsync is the ONLY way screens perform network I/O: work runs in a new
// goroutine with a bounded context, then done runs in the event loop via
// QueueUpdateDraw. done may freely update widgets.
func runAsync[T any](a *App, work func(ctx context.Context) (T, error), done func(T, error)) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), client.DefaultTimeout)
		defer cancel()
		v, err := work(ctx)
		a.tv.QueueUpdateDraw(func() { done(v, err) })
	}()
}

// globalKeys implements the F-key navigation (only after login).
func (a *App) globalKeys(ev *tcell.EventKey) *tcell.EventKey {
	if !a.loggedIn {
		return ev
	}
	switch ev.Key() {
	case tcell.KeyF2:
		a.showPage(pageBrowser)
	case tcell.KeyF3:
		a.showPage(pageLibrary)
	case tcell.KeyF4, tcell.KeyF5:
		a.setStatus(statusWarn, "This screen is not available yet.")
	case tcell.KeyF6:
		a.showPage(pageHelp)
	default:
		return ev
	}
	return nil
}

// showPage switches screens and gives focus to the page's default widget.
func (a *App) showPage(name string) {
	a.pages.RemovePage(pageUpdate) // leaving the library closes its dialog
	a.pages.SwitchToPage(name)
	switch name {
	case pageBrowser:
		a.browser.focus()
	case pageLibrary:
		a.library.open()
	case pageHelp:
		a.help.open()
	}
	a.refreshHeader()
}

// sessionExpired handles a 401 in the middle of a session: forget the token
// and go back to the login form. Returns true if err was a 401.
func (a *App) sessionExpired(err error) bool {
	if client.StatusOf(err) != http.StatusUnauthorized {
		return false
	}
	a.api.Logout()
	a.loggedIn = false
	a.library.reset()
	a.login.show("red", "Your session expired. Please log in again.")
	a.showPage(pageLogin)
	a.tv.SetFocus(a.login.form)
	return true
}

// onLoggedIn is called (in the event loop) after a successful login.
func (a *App) onLoggedIn(u client.User) {
	a.loggedIn = true
	a.setStatus(statusOK, fmt.Sprintf("Welcome, %s. Loading the catalog...", u.Username))
	a.showPage(pageBrowser)
	a.browser.search("")
}

func (a *App) refreshHeader() {
	who := "[gray]not logged in[-]"
	if a.loggedIn {
		who = "[green]" + tview.Escape(a.api.User().Username) + "[-]"
	}
	name, _ := a.pages.GetFrontPage()
	keys := "[gray]Ctrl+C quit[-]"
	if a.loggedIn {
		keys = "[gray]F2 Browse  F3 Library  F4 Chat  F5 Alerts  F6 Help  Ctrl+C quit[-]"
	}
	a.header.SetText(fmt.Sprintf(" [::b]MangaHub[::-] | %s | %s | %s", who, name, keys))
}

type statusLevel int

const (
	statusInfo statusLevel = iota
	statusOK
	statusWarn
	statusErr
)

// setStatus writes the bottom line. Event loop only.
func (a *App) setStatus(level statusLevel, msg string) {
	color := map[statusLevel]string{statusInfo: "white", statusOK: "green", statusWarn: "yellow", statusErr: "red"}[level]
	a.status.SetText(fmt.Sprintf(" [%s]%s[-]", color, tview.Escape(msg)))
}
