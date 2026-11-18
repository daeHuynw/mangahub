package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"mangahub/internal/tui/tcpclient"
)

// helpView is the Connection Status / Help screen: configured addresses, a
// live backend health check and the key bindings. TCP and UDP states are
// filled in once those clients exist.
type helpView struct {
	a      *App
	root   *tview.TextView
	health string // last health-check line (event loop only)
	seq    int
}

func newHelpView(a *App) *helpView {
	v := &helpView{a: a, root: tview.NewTextView().SetDynamicColors(true).SetWordWrap(true)}
	v.root.SetBorder(true).SetTitle(" Connection Status / Help (Esc or F2 to go back) ")
	v.root.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Key() == tcell.KeyEscape {
			a.showPage(pageBrowser)
			return nil
		}
		return ev
	})
	return v
}

// open redraws the page and starts a health check. Event loop only.
func (v *helpView) open() {
	v.a.tv.SetFocus(v.root)
	v.health = "[yellow]checking...[-]"
	v.render()
	v.seq++
	seq := v.seq
	api := v.a.api
	runAsync(v.a, func(ctx context.Context) (time.Duration, error) {
		start := time.Now()
		err := api.Health(ctx)
		return time.Since(start), err
	}, func(d time.Duration, err error) {
		if seq != v.seq {
			return
		}
		if err != nil {
			v.health = "[red]" + tview.Escape(friendlyError(err, api.BaseURL())) + "[-]"
		} else {
			v.health = fmt.Sprintf("[green]reachable[-] (GET /api/health in %s)", d.Round(time.Millisecond))
		}
		v.render()
	})
}

// syncLine describes the TCP session for the status screen.
func (v *helpView) syncLine() string {
	s := v.a.sync
	switch s.state {
	case tcpclient.Connected:
		line := fmt.Sprintf("[green]connected[-], session %s", tview.Escape(s.sessionID))
		if s.connects > 1 {
			line += fmt.Sprintf(" (reconnected %d time(s))", s.connects-1)
		}
		return line
	case tcpclient.Connecting:
		return "[yellow]connecting...[-]"
	case tcpclient.Disconnected:
		return "[red]disconnected[-], retrying: " + tview.Escape(s.lastErr)
	case tcpclient.AuthFailed:
		return "[red]token rejected[-]"
	}
	return "[gray]not connected[-]"
}

func (v *helpView) render() {
	var b strings.Builder
	fmt.Fprintf(&b, "[::b]Connections[::-]\n")
	fmt.Fprintf(&b, "  HTTP backend  %s  %s\n", tview.Escape(v.a.cfg.HTTP), v.health)
	fmt.Fprintf(&b, "  TCP sync      %s  %s\n", tview.Escape(v.a.cfg.TCP), v.syncLine())
	fmt.Fprintf(&b, "  UDP alerts    %s  [gray]not subscribed yet[-]\n", tview.Escape(v.a.cfg.UDP))
	fmt.Fprintf(&b, "  Catalog       reached only through the backend (TUI -> HTTP -> Gin -> gRPC)\n\n")
	fmt.Fprintf(&b, "[::b]Keys[::-]\n")
	for _, k := range [][2]string{
		{"F2", "Manga Browser"}, {"F3", "My Library"}, {"F4", "Discussion"},
		{"F5", "Notifications"}, {"F6", "this screen"},
		{"a", "add the selected manga to your library (browser)"},
		{"Enter / + / -", "edit chapter, or one chapter up/down (library)"},
		{"/", "focus the search box"}, {"Tab / Shift+Tab", "next / previous pane"},
		{"Up / Down", "move in a list"}, {"Enter", "confirm / open details"},
		{"Esc", "back"}, {"Ctrl+C", "quit"},
	} {
		fmt.Fprintf(&b, "  [yellow]%-16s[-] %s\n", k[0], k[1])
	}
	fmt.Fprintf(&b, "\nAddresses come from MANGAHUB_HTTP / MANGAHUB_TCP / MANGAHUB_UDP.\n")
	v.root.SetText(b.String())
}
