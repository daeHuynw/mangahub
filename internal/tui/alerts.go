package tui

import (
	"context"
	"fmt"
	"time"

	"github.com/rivo/tview"

	"mangahub/internal/tui/udpclient"
)

// alertState is what the UI knows about the UDP subscription. Event loop only.
type alertState struct {
	state   udpclient.State
	lastErr string
	gen     int
	cancel  context.CancelFunc
	done    chan struct{} // closed when udpclient.Run returned (unsubscribe sent)
}

// startAlerts subscribes to new-chapter alerts after login. Event loop only.
func (a *App) startAlerts() {
	a.stopAlerts()
	a.alerts.gen++
	gen := a.alerts.gen
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	a.alerts.cancel, a.alerts.done = cancel, done
	a.alerts.state = udpclient.Subscribing

	cfg := udpclient.Config{Addr: a.cfg.UDP, Token: a.api.Token}
	h := udpclient.Handler{
		OnState: func(s udpclient.State, err error) {
			a.post(func() {
				if gen == a.alerts.gen {
					a.onAlertState(s, err)
				}
			})
		},
		OnChapter: func(c udpclient.Chapter) {
			a.post(func() {
				if gen == a.alerts.gen {
					a.notifications.add(c)
				}
			})
		},
	}
	go func() {
		defer close(done)
		if err := udpclient.Run(ctx, cfg, h); err != nil {
			a.post(func() {
				if gen == a.alerts.gen {
					a.onAlertState(udpclient.Rejected, err)
				}
			})
		}
	}()
}

// stopAlerts unsubscribes (logout, expired session). It does not wait for
// the unsubscribe datagram; waitAlerts does that on quit.
func (a *App) stopAlerts() {
	if a.alerts.cancel != nil {
		a.alerts.cancel()
		a.alerts.cancel = nil
	}
	a.alerts.gen++
	a.alerts.state = udpclient.Stopped
}

// waitAlerts gives the UDP goroutine a moment to send unsubscribe before the
// process exits, so the server does not keep a dead address for 90 s.
func (a *App) waitAlerts(max time.Duration) {
	if a.alerts.done == nil {
		return
	}
	select {
	case <-a.alerts.done:
	case <-time.After(max):
	}
}

func (a *App) onAlertState(s udpclient.State, err error) {
	a.alerts.state = s
	if err != nil {
		a.alerts.lastErr = err.Error()
	}
	switch s {
	case udpclient.NoAck:
		a.setStatus(statusWarn, "Chapter alerts: no answer from the server yet, will keep trying.")
	case udpclient.Rejected:
		a.setStatus(statusErr, "Chapter alerts: the server rejected the subscription.")
	}
	a.refreshHeader()
	if name, _ := a.pages.GetFrontPage(); name == pageHelp {
		a.help.render()
	}
}

func (a *App) alertsLabel() string {
	unread := ""
	if n := a.notifications.unread; n > 0 {
		unread = fmt.Sprintf(" [yellow::b](%d new)[-::-]", n)
	}
	switch a.alerts.state {
	case udpclient.Subscribed:
		return "[green]alerts on[-]" + unread
	case udpclient.Subscribing:
		return "[yellow]alerts subscribing[-]" + unread
	case udpclient.NoAck:
		return "[red]alerts no ack[-]" + unread
	case udpclient.Rejected:
		return "[red]alerts rejected[-]" + unread
	}
	return "[gray]alerts off[-]" + unread
}

// ---------------------------------------------------------------- screen

type alertItem struct {
	at time.Time
	c  udpclient.Chapter
}

// notificationsView is the Notifications screen (F5): every new_chapter
// datagram received in this session, newest first.
type notificationsView struct {
	a      *App
	root   *tview.Flex
	table  *tview.Table
	items  []alertItem
	unread int
}

func newNotificationsView(a *App) *notificationsView {
	v := &notificationsView{a: a, table: tview.NewTable().SetSelectable(true, false).SetFixed(1, 0)}
	v.table.SetBorder(true).SetTitle(" Notifications ")
	note := tview.NewTextView().SetDynamicColors(true).
		SetText("[gray]New-chapter alerts arrive over UDP: best effort, a lost datagram is not resent. F3 shows the current counts.[-]")
	v.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(v.table, 0, 1, true).
		AddItem(note, 1, 0, false)
	v.render()
	return v
}

func (v *notificationsView) open() {
	v.unread = 0
	v.render()
	v.a.tv.SetFocus(v.table)
	v.a.refreshHeader()
}

func (v *notificationsView) reset() {
	v.items, v.unread = nil, 0
	v.render()
}

// add records one alert and updates every screen that shows chapter counts.
func (v *notificationsView) add(c udpclient.Chapter) {
	v.items = append([]alertItem{{at: time.Now(), c: c}}, v.items...)
	if name, _ := v.a.pages.GetFrontPage(); name != pageNotify {
		v.unread++
	}
	v.render()

	title := c.Title
	if title == "" {
		title = c.MangaID
	}
	v.a.setStatus(statusOK, fmt.Sprintf("New chapter: %s now has %d chapters. F5 for all alerts.", title, c.TotalChapters))
	if v.a.library.state.setTotal(c.MangaID, c.TotalChapters) {
		v.a.library.render()
	}
	v.a.browser.updateTotal(c.MangaID, c.TotalChapters)
	v.a.refreshHeader()
}

func (v *notificationsView) render() {
	v.table.Clear()
	for col, h := range []string{"Time", "Manga", "Chapters"} {
		v.table.SetCell(0, col, tview.NewTableCell("[yellow::b]"+h).SetSelectable(false).SetExpansion(boolInt(col == 1)))
	}
	for i, it := range v.items {
		title := it.c.Title
		if title == "" {
			title = it.c.MangaID
		}
		v.table.SetCell(i+1, 0, tview.NewTableCell(it.at.Format("15:04:05")))
		v.table.SetCell(i+1, 1, tview.NewTableCell(tview.Escape(title)).SetExpansion(1))
		v.table.SetCell(i+1, 2, tview.NewTableCell(fmt.Sprintf("%d", it.c.TotalChapters)).SetAlign(tview.AlignRight))
	}
	if len(v.items) == 0 {
		v.table.SetCell(1, 0, tview.NewTableCell("[gray]No alerts yet.").SetSelectable(false))
	}
	v.table.SetTitle(fmt.Sprintf(" Notifications (%d) ", len(v.items)))
	v.table.Select(1, 0)
}
