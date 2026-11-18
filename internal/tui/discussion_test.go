package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"mangahub/internal/tui/wsclient"
)

func TestWSURL(t *testing.T) {
	cases := map[string]string{
		"http://localhost:8080":    "ws://localhost:8080/ws",
		"http://localhost:8080/":   "ws://localhost:8080/ws",
		"https://example.test":     "wss://example.test/ws",
		"http://10.0.0.5:9000/abc": "ws://10.0.0.5:9000/abc/ws",
	}
	for in, want := range cases {
		if got := wsURL(in); got != want {
			t.Errorf("wsURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// chatText is what the Discussion screen shows (event loop only).
func (h *harness) chatText() string { return h.a.discussion.messages.GetText(true) }

func (h *harness) say(text string) {
	h.typeText(text)
	h.key(tcell.KeyEnter)
}

// F05 through the TUI: Alice and Bob talk in general, Charlie is in another
// room and sees none of it, Bob logs out and Alice's room shows it.
func TestUI_DiscussionRoomsAndDisconnect(t *testing.T) {
	alice := start(t)
	alice.login("alice", "secret123")
	bob := alice.another()
	bob.login("bob", "secret123")
	charlie := alice.another()
	charlie.login("charlie", "secret123")
	for _, h := range []*harness{alice, bob, charlie} {
		h.waitFor("chat connected", func() bool {
			return h.a.chat.state == wsclient.Connected && strings.Contains(h.a.header.GetText(true), "chat on")
		})
		h.key(tcell.KeyF4)
		h.waitFor("discussion page", func() bool { p, _ := h.a.pages.GetFrontPage(); return p == pageChat })
	}
	if n := alice.chat.Members("general"); n != 3 {
		t.Fatalf("general has %d members, want 3", n)
	}

	// Charlie moves to another room with /join.
	charlie.say("/join one-piece")
	charlie.waitFor("charlie in one-piece", func() bool {
		return charlie.a.chat.client.Room() == "one-piece" && strings.Contains(charlie.chatText(), "charlie joined") &&
			strings.Contains(charlie.a.discussion.messages.GetTitle(), "#one-piece")
	})
	alice.waitFor("alice sees charlie leave", func() bool { return strings.Contains(alice.chatText(), "charlie left") })

	// Alice talks; Bob sees it with Alice's name, Charlie does not.
	alice.say("Chapter 1131 is out!")
	bob.waitFor("bob got it", func() bool { return strings.Contains(bob.chatText(), "alice: Chapter 1131 is out!") })
	alice.waitFor("alice sees her own line", func() bool { return strings.Contains(alice.chatText(), "alice: Chapter 1131") })

	// Charlie talks in his room; Alice does not see it.
	charlie.say("one-piece only")
	charlie.waitFor("charlie's own line", func() bool { return strings.Contains(charlie.chatText(), "charlie: one-piece only") })
	bob.say("marker")
	alice.waitFor("marker", func() bool { return strings.Contains(alice.chatText(), "bob: marker") })
	var aliceView, charlieView string
	alice.ui(func() { aliceView = alice.chatText() })
	charlie.ui(func() { charlieView = charlie.chatText() })
	if strings.Contains(aliceView, "one-piece only") || strings.Contains(charlieView, "1131") {
		t.Fatalf("room isolation broken:\nalice:\n%s\ncharlie:\n%s", aliceView, charlieView)
	}

	// Typing while messages arrive: the half-written line is kept.
	alice.typeText("half writ")
	bob.say("interrupt")
	alice.waitFor("message arrived during typing", func() bool { return strings.Contains(alice.chatText(), "bob: interrupt") })
	alice.ui(func() {
		if got := alice.a.discussion.input.GetText(); got != "half writ" {
			t.Errorf("input = %q, want the half-written text kept", got)
		}
	})

	// Bob logs out: Alice sees "bob left", the hub keeps working.
	bob.screen.InjectKey(tcell.KeyCtrlL, 0, tcell.ModCtrl)
	alice.waitFor("bob left", func() bool { return strings.Contains(alice.chatText(), "bob left") })
	alice.ui(func() { alice.a.discussion.input.SetText("") })
	alice.say("still working")
	alice.waitFor("still working", func() bool { return strings.Contains(alice.chatText(), "alice: still working") })
}

func TestUI_DiscussionValidation(t *testing.T) {
	h := start(t)
	h.login("alice", "secret123")
	h.waitFor("chat connected", func() bool { return h.a.chat.state == wsclient.Connected })
	h.key(tcell.KeyF4)
	h.waitFor("page", func() bool { p, _ := h.a.pages.GetFrontPage(); return p == pageChat })

	h.say("/join Bad Room!")
	h.waitFor("bad room rejected locally", func() bool {
		return strings.Contains(h.a.status.GetText(true), "Room names use lowercase") && h.a.chat.client.Room() == "general"
	})
	h.ui(func() { h.a.discussion.input.SetText(strings.Repeat("x", 501)) }) // typing 501 keys is slow
	h.key(tcell.KeyEnter)
	h.waitFor("too long", func() bool { return strings.Contains(h.a.status.GetText(true), "too long") })
	h.ui(func() { h.a.discussion.input.SetText("") })

	// Enter on an empty line sends nothing.
	h.key(tcell.KeyEnter)
	time.Sleep(50 * time.Millisecond)
	var text string
	h.ui(func() { text = h.chatText() })
	if strings.Contains(text, "alice: ") {
		t.Fatalf("an empty line was sent:\n%s", text)
	}
}

// Rooms list = general + one room per manga in the library; Enter joins.
func TestUI_DiscussionRoomListFromLibrary(t *testing.T) {
	h := start(t)
	h.login("alice", "secret123")
	h.waitFor("catalog", func() bool { return h.a.browser.list.GetItemCount() == 3 })
	h.typeText("a")
	h.waitFor("added", func() bool { return strings.Contains(h.a.status.GetText(true), "Added One Piece") })
	h.key(tcell.KeyF3)
	h.waitFor("library loaded", func() bool { return h.a.library.loaded })
	h.key(tcell.KeyF4)
	h.waitFor("rooms", func() bool {
		return strings.Join(h.a.discussion.roomList, ",") == "general,one-piece" && h.a.chat.state == wsclient.Connected
	})
	h.key(tcell.KeyTab)  // input -> rooms
	h.key(tcell.KeyDown) // general -> one-piece
	h.key(tcell.KeyEnter)
	h.waitFor("joined one-piece", func() bool {
		return h.a.chat.client.Room() == "one-piece" && h.chat.Members("one-piece") == 1 &&
			strings.Contains(h.chatText(), "alice joined")
	})
}
