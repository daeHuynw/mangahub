package tui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"mangahub/internal/tui/client"
)

// libraryView is My Library (F3) plus the Update Chapter dialog.
//
// The rows live in libraryState; this file only renders them and turns key
// presses into HTTP calls. Every change that comes back (our own PUT, and
// later TCP events from other sessions) goes through state.apply, which keeps
// only the highest version.
type libraryView struct {
	a     *App
	root  *tview.Flex
	table *tview.Table
	state libraryState

	loaded  bool // state holds a successful GET /api/library
	loadSeq int

	dialog     tview.Primitive
	form       *tview.Form
	chapter    *tview.InputField
	status     *tview.DropDown
	formMsg    *tview.TextView
	editing    string // manga id open in the dialog
	saving     bool
	pendingAdd map[string]bool // manga ids with an add request in flight
}

func newLibraryView(a *App) *libraryView {
	v := &libraryView{
		a:          a,
		table:      tview.NewTable().SetSelectable(true, false).SetFixed(1, 0),
		pendingAdd: map[string]bool{},
	}
	v.table.SetBorder(true).SetTitle(" My Library ")
	v.table.SetSelectedFunc(func(row, _ int) { v.openDialog(row) })
	v.table.SetInputCapture(v.keys)

	help := tview.NewTextView().SetDynamicColors(true).
		SetText("[gray]Enter edit chapter/status  + / - one chapter  r reload  F2 back to browser[-]")
	v.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(v.table, 0, 1, true).
		AddItem(help, 1, 0, false)

	v.buildDialog()
	v.render()
	return v
}

func (v *libraryView) buildDialog() {
	v.chapter = tview.NewInputField().SetLabel("Chapter").SetFieldWidth(8).
		SetAcceptanceFunc(tview.InputFieldInteger)
	v.status = tview.NewDropDown().SetLabel("Status").SetOptions(client.LibraryStatuses, nil)
	v.formMsg = tview.NewTextView().SetDynamicColors(true)
	v.form = tview.NewForm().
		AddFormItem(v.chapter).
		AddFormItem(v.status).
		AddButton("Save", v.save).
		AddButton("Cancel", v.closeDialog)
	v.form.SetCancelFunc(v.closeDialog) // Esc
	v.form.SetBorder(true).SetTitle(" Update chapter ")

	box := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(v.form, 9, 0, true).
		AddItem(v.formMsg, 2, 0, false).
		AddItem(nil, 0, 1, false)
	v.dialog = tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(box, 52, 0, true).
		AddItem(nil, 0, 1, false)
}

// open is called when F3 shows the page.
func (v *libraryView) open() {
	v.a.tv.SetFocus(v.table)
	v.reload()
}

// reset forgets everything (logout / expired session).
func (v *libraryView) reset() {
	v.state.set(nil)
	v.loaded = false
	v.loadSeq++
	v.editing = ""
	v.render()
}

func (v *libraryView) keys(ev *tcell.EventKey) *tcell.EventKey {
	if ev.Key() != tcell.KeyRune {
		return ev
	}
	switch ev.Rune() {
	case '+', '-':
		it, ok := v.selected()
		if !ok {
			return nil
		}
		step := 1
		if ev.Rune() == '-' {
			step = -1
		}
		ch := it.CurrentChapter + step
		if ch < 0 || ch > it.TotalChapters {
			v.a.setStatus(statusWarn, fmt.Sprintf("Chapter must stay between 0 and %d.", it.TotalChapters))
			return nil
		}
		v.update(it, ch, "", nil)
		return nil
	case 'r':
		v.reload()
		return nil
	}
	return ev
}

// selected returns the row under the cursor.
func (v *libraryView) selected() (client.LibraryItem, bool) {
	row, _ := v.table.GetSelection()
	i := row - 1 // row 0 is the header
	if i < 0 || i >= len(v.state.items) {
		return client.LibraryItem{}, false
	}
	return v.state.items[i], true
}

// reload fetches GET /api/library. Event loop only.
func (v *libraryView) reload() {
	v.loadSeq++
	seq := v.loadSeq
	v.table.SetTitle(" My Library (loading...) ")
	api := v.a.api
	runAsync(v.a, func(ctx context.Context) ([]client.LibraryItem, error) {
		return api.ListLibrary(ctx)
	}, func(items []client.LibraryItem, err error) {
		if seq != v.loadSeq {
			return
		}
		if err != nil {
			v.table.SetTitle(" My Library ")
			if !v.a.sessionExpired(err) {
				v.a.setStatus(statusErr, libraryError(err, api.BaseURL()))
			}
			return
		}
		v.state.set(items)
		v.loaded = true
		v.render()
		if len(items) == 0 {
			v.a.setStatus(statusInfo, "Your library is empty. In the browser (F2) press a on a manga to add it.")
		} else {
			v.a.setStatus(statusOK, fmt.Sprintf("%d manga in your library.", len(items)))
		}
	})
}

// render redraws the table from state, keeping the cursor on the same manga.
func (v *libraryView) render() {
	keep, _ := v.selected()
	v.table.Clear()
	for col, h := range []string{"Title", "Status", "Chapter", "Version", "Updated"} {
		v.table.SetCell(0, col, tview.NewTableCell("[yellow::b]"+h).SetSelectable(false).SetExpansion(boolInt(col == 0)))
	}
	sel := 1
	for i, it := range v.state.items {
		r := i + 1
		v.table.SetCell(r, 0, tview.NewTableCell(tview.Escape(it.Title)).SetExpansion(1))
		v.table.SetCell(r, 1, tview.NewTableCell(statusColor(it.Status)+it.Status))
		v.table.SetCell(r, 2, tview.NewTableCell(fmt.Sprintf("%d / %d", it.CurrentChapter, it.TotalChapters)).SetAlign(tview.AlignRight))
		v.table.SetCell(r, 3, tview.NewTableCell("v"+strconv.FormatInt(it.Version, 10)).SetAlign(tview.AlignRight))
		v.table.SetCell(r, 4, tview.NewTableCell(shortTime(it.UpdatedAt)))
		if it.MangaID == keep.MangaID {
			sel = r
		}
	}
	if len(v.state.items) == 0 {
		v.table.SetCell(1, 0, tview.NewTableCell("[gray](empty)").SetSelectable(false))
	}
	v.table.Select(sel, 0)
	v.table.SetTitle(fmt.Sprintf(" My Library (%d) ", len(v.state.items)))
}

// openDialog shows the Update Chapter form for table row `row`.
func (v *libraryView) openDialog(row int) {
	i := row - 1
	if i < 0 || i >= len(v.state.items) {
		return
	}
	it := v.state.items[i]
	v.editing = it.MangaID
	v.form.SetTitle(fmt.Sprintf(" %s (0-%d) ", it.Title, it.TotalChapters))
	v.chapter.SetText(strconv.Itoa(it.CurrentChapter))
	for k, s := range client.LibraryStatuses {
		if s == it.Status {
			v.status.SetCurrentOption(k)
		}
	}
	v.formMsg.SetText("[gray]Tab moves, Save with Enter, Esc cancels.[-]")
	v.a.pages.AddPage(pageUpdate, v.dialog, true, true) // overlay on top of the library
	v.form.SetFocus(0)
	v.a.tv.SetFocus(v.form)
}

func (v *libraryView) closeDialog() {
	v.editing = ""
	v.saving = false
	v.a.pages.RemovePage(pageUpdate)
	v.a.tv.SetFocus(v.table)
}

// save validates the form and sends PUT .../progress. Event loop only.
func (v *libraryView) save() {
	if v.saving {
		return
	}
	it, ok := v.state.get(v.editing)
	if !ok {
		v.closeDialog()
		return
	}
	ch, msg := parseChapter(v.chapter.GetText(), it.TotalChapters)
	if msg != "" {
		v.formError(msg)
		return
	}
	_, status := v.status.GetCurrentOption()
	if ch == it.CurrentChapter && status == it.Status {
		v.closeDialog()
		return
	}
	v.saving = true
	v.formMsg.SetText("[yellow]Saving...[-]")
	v.update(it, ch, status, func(err error) {
		v.saving = false
		if err != nil {
			if v.editing != "" { // dialog still open (a 401 closes everything)
				v.formError(libraryError(err, v.a.api.BaseURL()))
			}
			return
		}
		v.closeDialog()
	})
}

// formError shows msg under the dialog and puts the cursor back in the
// chapter field, so the user can fix the number right away.
func (v *libraryView) formError(msg string) {
	v.formMsg.SetText("[red]" + tview.Escape(msg) + "[-]")
	v.form.SetFocus(0)
	v.a.tv.SetFocus(v.form)
}

// update sends one progress change and merges the answer into state.
// done (optional) runs after the answer is applied.
func (v *libraryView) update(it client.LibraryItem, chapter int, status string, done func(error)) {
	api := v.a.api
	v.a.setStatus(statusInfo, fmt.Sprintf("Saving %s chapter %d...", it.Title, chapter))
	runAsync(v.a, func(ctx context.Context) (client.Progress, error) {
		return api.UpdateProgress(ctx, it.MangaID, chapter, status)
	}, func(p client.Progress, err error) {
		if err != nil {
			if v.a.sessionExpired(err) {
				return
			}
			v.a.setStatus(statusErr, libraryError(err, api.BaseURL()))
			if client.StatusOf(err) == http.StatusNotFound {
				v.reload() // the row is gone on the server
			}
			if done != nil {
				done(err)
			}
			return
		}
		v.state.apply(p)
		v.render()
		v.a.setStatus(statusOK, fmt.Sprintf("Saved %s: chapter %d, %s (v%d).", it.Title, p.CurrentChapter, p.Status, p.Version))
		if done != nil {
			done(nil)
		}
	})
}

// add puts a manga in the library (from the browser). Event loop only.
func (v *libraryView) add(m client.Manga) {
	if v.pendingAdd[m.ID] {
		return
	}
	v.pendingAdd[m.ID] = true
	api := v.a.api
	v.a.setStatus(statusInfo, fmt.Sprintf("Adding %s to your library...", m.Title))
	runAsync(v.a, func(ctx context.Context) (client.LibraryItem, error) {
		return api.AddToLibrary(ctx, m.ID, "")
	}, func(it client.LibraryItem, err error) {
		delete(v.pendingAdd, m.ID)
		if err != nil {
			if !v.a.sessionExpired(err) {
				v.a.setStatus(statusErr, libraryError(err, api.BaseURL()))
			}
			return
		}
		if v.loaded {
			v.state.add(it)
			v.render()
		}
		v.a.setStatus(statusOK, fmt.Sprintf("Added %s to your library (%s). F3 opens My Library.", it.Title, it.Status))
	})
}

// libraryError words library-specific statuses; the rest is friendlyError.
func libraryError(err error, backend string) string {
	var ae *client.APIError
	if errors.As(err, &ae) {
		switch ae.Status {
		case http.StatusNotFound:
			return "Not found: " + ae.Message + "."
		case http.StatusConflict:
			return "This manga is already in your library."
		}
	}
	return friendlyError(err, backend)
}

func statusColor(s string) string {
	switch s {
	case client.StatusReading:
		return "[green]"
	case client.StatusCompleted:
		return "[blue]"
	}
	return "[white]"
}

// shortTime shows an RFC3339 timestamp in local time, or the raw text.
func shortTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	return t.Local().Format("2006-01-02 15:04")
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
