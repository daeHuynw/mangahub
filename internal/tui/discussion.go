package tui

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"mangahub/internal/tui/wsclient"
)

// chatState is what the UI knows about the WebSocket. Event loop only.
type chatState struct {
	state  wsclient.State
	gen    int
	cancel context.CancelFunc
	client *wsclient.Client
}

// startChat opens the WebSocket after login. Event loop only.
func (a *App) startChat() {
	a.stopChat()
	a.chat.gen++
	gen := a.chat.gen
	ctx, cancel := context.WithCancel(context.Background())
	c := wsclient.New(wsclient.Config{URL: wsURL(a.cfg.HTTP), Token: a.api.Token}, wsclient.Handler{
		OnState: func(s wsclient.State, err error) {
			a.post(func() {
				if gen == a.chat.gen {
					a.onChatState(s, err)
				}
			})
		},
		OnMessage: func(m wsclient.Message) {
			a.post(func() {
				if gen == a.chat.gen {
					a.discussion.receive(m)
				}
			})
		},
	})
	a.chat.cancel, a.chat.client = cancel, c
	a.chat.state = wsclient.Connecting
	go c.Run(ctx)
}

func (a *App) stopChat() {
	if a.chat.cancel != nil {
		a.chat.cancel()
		a.chat.cancel = nil
	}
	a.chat.client = nil
	a.chat.gen++
	a.chat.state = wsclient.Stopped
}

func (a *App) onChatState(s wsclient.State, err error) {
	prev := a.chat.state
	a.chat.state = s
	switch {
	case s == wsclient.Disconnected && prev == wsclient.Connected:
		a.discussion.note("[red]* connection lost, reconnecting...[-]")
	case s == wsclient.Connected && prev != wsclient.Connecting:
		a.discussion.note("[green]* reconnected[-]")
	case s == wsclient.Rejected:
		a.setStatus(statusErr, "Chat: the server rejected the token.")
	}
	a.discussion.renderTitle()
	a.refreshHeader()
	if name, _ := a.pages.GetFrontPage(); name == pageHelp {
		a.help.render()
	}
}

func (a *App) chatLabel() string {
	switch a.chat.state {
	case wsclient.Connected:
		return "[green]chat on[-]"
	case wsclient.Connecting:
		return "[yellow]chat connecting[-]"
	case wsclient.Disconnected, wsclient.Rejected:
		return "[red]chat off[-]"
	}
	return "[gray]chat off[-]"
}

// wsURL turns the backend's HTTP base into the WebSocket URL:
// http://host:8080 → ws://host:8080/ws, https → wss.
func wsURL(httpBase string) string {
	u := strings.TrimRight(httpBase, "/")
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + strings.TrimPrefix(u, "http://")
	}
	return u + "/ws"
}

// ---------------------------------------------------------------- screen

// chatRoomRe mirrors the server's rule so a bad name is reported at once.
var chatRoomRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

const maxHistory = 300 // lines kept per room

// discussionView is the Discussion screen (F4): rooms on the left, the
// current room's messages on the right, an input line at the bottom.
type discussionView struct {
	a        *App
	root     *tview.Flex
	rooms    *tview.List
	messages *tview.TextView
	input    *tview.InputField

	history  map[string][]string // room → rendered lines
	roomList []string
}

func newDiscussionView(a *App) *discussionView {
	v := &discussionView{
		a:        a,
		rooms:    tview.NewList().ShowSecondaryText(false).SetHighlightFullLine(true),
		messages: tview.NewTextView().SetDynamicColors(true).SetWordWrap(true).SetScrollable(true),
		input:    tview.NewInputField().SetLabel("> ").SetPlaceholder("message, Enter to send   /join <room> to switch"),
		history:  map[string][]string{},
	}
	v.rooms.SetBorder(true).SetTitle(" Rooms ")
	v.rooms.SetSelectedFunc(func(i int, _, _ string, _ rune) {
		if i >= 0 && i < len(v.roomList) {
			v.join(v.roomList[i])
			v.a.tv.SetFocus(v.input)
		}
	})
	// No SetChangedFunc here: tview calls it from its own goroutine, so
	// scrolling in it would touch the widget outside the event loop (the race
	// detector caught exactly that). render() scrolls to the end instead.
	v.messages.SetBorder(true)
	v.input.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			v.submit()
		}
	})

	right := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(v.messages, 0, 1, false).
		AddItem(v.input, 1, 0, true)
	help := tview.NewTextView().SetDynamicColors(true).
		SetText("[gray]Enter send  Tab rooms/input  Enter on a room to join  /join <room>  Esc back to input[-]")
	body := tview.NewFlex().
		AddItem(v.rooms, 24, 0, false).
		AddItem(right, 0, 1, true)
	v.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(body, 0, 1, true).
		AddItem(help, 1, 0, false)
	v.root.SetInputCapture(v.keys)
	v.renderTitle()
	return v
}

func (v *discussionView) keys(ev *tcell.EventKey) *tcell.EventKey {
	switch ev.Key() {
	case tcell.KeyTab:
		if v.a.tv.GetFocus() == v.input {
			v.a.tv.SetFocus(v.rooms)
		} else {
			v.a.tv.SetFocus(v.input)
		}
		return nil
	case tcell.KeyEscape:
		v.a.tv.SetFocus(v.input)
		return nil
	}
	return ev
}

// currentRoom is where the user is (or will be after a reconnect).
func (v *discussionView) currentRoom() string {
	if c := v.a.chat.client; c != nil {
		return c.Room()
	}
	return wsclient.DefaultRoom
}

// open is called when F4 shows the page.
func (v *discussionView) open() {
	v.refreshRooms()
	v.render()
	v.a.tv.SetFocus(v.input)
}

func (v *discussionView) reset() {
	v.history = map[string][]string{}
	v.input.SetText("")
	v.render()
}

// refreshRooms lists general, one room per manga in the library, and any
// room the user joined by hand.
func (v *discussionView) refreshRooms() {
	set := map[string]bool{wsclient.DefaultRoom: true, v.currentRoom(): true}
	for _, it := range v.a.library.state.items {
		set[it.MangaID] = true
	}
	for room := range v.history {
		set[room] = true
	}
	delete(set, "")
	others := make([]string, 0, len(set))
	for r := range set {
		if r != wsclient.DefaultRoom {
			others = append(others, r)
		}
	}
	sort.Strings(others)
	v.roomList = append([]string{wsclient.DefaultRoom}, others...)
	cur := v.currentRoom()
	v.rooms.Clear()
	for i, r := range v.roomList {
		label := r
		if r == cur {
			label = "[green]> " + r + "[-]"
		}
		v.rooms.AddItem(label, "", 0, nil)
		if r == cur {
			v.rooms.SetCurrentItem(i)
		}
	}
}

// submit sends the input line (or handles /join).
func (v *discussionView) submit() {
	text := strings.TrimSpace(v.input.GetText())
	if text == "" {
		return
	}
	if strings.HasPrefix(text, "/join") {
		room := strings.TrimSpace(strings.TrimPrefix(text, "/join"))
		v.input.SetText("")
		v.join(room)
		return
	}
	if len([]rune(text)) > 500 {
		v.a.setStatus(statusWarn, "Message is too long (500 characters max).")
		return
	}
	c := v.a.chat.client
	if c == nil {
		return
	}
	if err := c.Say(text); err != nil {
		v.a.setStatus(statusErr, "Chat is not connected; the message was not sent.")
		return
	}
	v.input.SetText("") // the line shows up when the server echoes it back
}

func (v *discussionView) join(room string) {
	room = strings.ToLower(strings.TrimSpace(room))
	if !chatRoomRe.MatchString(room) {
		v.a.setStatus(statusWarn, "Room names use lowercase letters, digits and dashes (max 40).")
		return
	}
	c := v.a.chat.client
	if c == nil || room == c.Room() {
		return
	}
	if err := c.Join(room); err != nil {
		v.a.setStatus(statusWarn, "Chat is offline; you will join "+room+" when it reconnects.")
	}
	v.refreshRooms()
	v.render()
}

// receive handles one message from the server. Event loop only.
func (v *discussionView) receive(m wsclient.Message) {
	room := m.Room
	if room == "" {
		room = v.currentRoom() // errors carry no room: show them where the user is
	}
	at := clock(m.At)
	var line string
	switch m.Type {
	case "chat":
		line = fmt.Sprintf("[gray]%s[-] [yellow]%s[-]: %s", at, tview.Escape(m.From), tview.Escape(m.Text))
	case "system":
		line = fmt.Sprintf("[gray]%s * %s[-]", at, tview.Escape(m.Text))
	case "error":
		line = "[red]! " + tview.Escape(m.Text) + "[-]"
		v.a.setStatus(statusErr, "Chat: "+m.Text)
	default:
		return
	}
	v.add(room, line)
	if _, known := v.history[room]; known && !v.inList(room) {
		v.refreshRooms()
	}
}

// note adds a local line (connection changes) to the current room.
func (v *discussionView) note(line string) { v.add(v.currentRoom(), line) }

func (v *discussionView) add(room, line string) {
	h := append(v.history[room], line)
	if len(h) > maxHistory {
		h = h[len(h)-maxHistory:]
	}
	v.history[room] = h
	if room == v.currentRoom() {
		v.render()
	}
}

func (v *discussionView) inList(room string) bool {
	for _, r := range v.roomList {
		if r == room {
			return true
		}
	}
	return false
}

func (v *discussionView) render() {
	v.messages.SetText(strings.Join(v.history[v.currentRoom()], "\n"))
	v.messages.ScrollToEnd()
	v.renderTitle()
}

func (v *discussionView) renderTitle() {
	v.messages.SetTitle(fmt.Sprintf(" #%s  (%s) ", v.currentRoom(), v.a.chat.state))
}

// clock shows an RFC3339 time as local HH:MM:SS.
func clock(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now().Format("15:04:05")
	}
	return t.Local().Format("15:04:05")
}
