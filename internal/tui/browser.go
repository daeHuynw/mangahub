package tui

import (
	"context"
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"mangahub/internal/tui/client"
)

// browserView is the Manga Browser (search + genre filter + list) with the
// Manga Details panel on the right.
//
// All fields are owned by the event loop. Requests are numbered (searchSeq,
// detailSeq): when a response arrives, it is applied only if it is still the
// latest request, so scrolling quickly through the list never shows the
// details of a manga that is no longer selected.
type browserView struct {
	a       *App
	root    *tview.Flex
	query   *tview.InputField
	genre   *tview.DropDown
	list    *tview.List
	details *tview.TextView

	results   []client.Manga // last search result
	shown     []client.Manga // results filtered by genre = list rows
	genres    []string       // genre options (from the full catalog)
	curGenre  string
	searchSeq int
	detailSeq int
	rebuild   bool // true while the list is being refilled (ignore change events)
}

func newBrowserView(a *App) *browserView {
	v := &browserView{
		a:        a,
		query:    tview.NewInputField().SetLabel("Search: ").SetPlaceholder("title, author or genre - Enter"),
		genre:    tview.NewDropDown().SetLabel("  Genre: "),
		list:     tview.NewList().ShowSecondaryText(false).SetHighlightFullLine(true),
		details:  tview.NewTextView().SetDynamicColors(true).SetWordWrap(true),
		curGenre: allGenres,
	}
	v.query.SetDoneFunc(func(key tcell.Key) {
		switch key {
		case tcell.KeyEnter:
			v.search(v.query.GetText())
		case tcell.KeyEscape, tcell.KeyDown:
			v.a.tv.SetFocus(v.list)
		}
	})
	v.setGenreOptions(nil)

	v.list.SetBorder(true).SetTitle(" Manga ")
	v.list.SetChangedFunc(func(i int, _, _ string, _ rune) {
		if !v.rebuild {
			v.loadDetails(i)
		}
	})
	v.list.SetSelectedFunc(func(int, string, string, rune) { v.a.tv.SetFocus(v.details) })
	v.details.SetBorder(true).SetTitle(" Details ")
	v.details.SetText("[gray]Select a manga to see its details.[-]")

	top := tview.NewFlex().AddItem(v.query, 0, 2, true).AddItem(v.genre, 30, 0, false)
	body := tview.NewFlex().AddItem(v.list, 0, 1, false).AddItem(v.details, 0, 1, false)
	help := tview.NewTextView().SetDynamicColors(true).
		SetText("[gray]/ search  Tab next pane  Up/Down select  a add to library  Enter open details  Esc back to list[-]")
	v.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(top, 1, 0, true).
		AddItem(body, 0, 1, false).
		AddItem(help, 1, 0, false)
	v.root.SetInputCapture(v.keys)
	return v
}

// focusOrder is the Tab cycle inside the browser.
func (v *browserView) focusOrder() []tview.Primitive {
	return []tview.Primitive{v.query, v.genre, v.list, v.details}
}

func (v *browserView) focus() { v.a.tv.SetFocus(v.list) }

func (v *browserView) keys(ev *tcell.EventKey) *tcell.EventKey {
	focused := v.a.tv.GetFocus()
	switch {
	case ev.Key() == tcell.KeyTab || ev.Key() == tcell.KeyBacktab:
		order := v.focusOrder()
		idx := 0
		for i, p := range order {
			if p == focused {
				idx = i
			}
		}
		step := 1
		if ev.Key() == tcell.KeyBacktab {
			step = len(order) - 1
		}
		v.a.tv.SetFocus(order[(idx+step)%len(order)])
		return nil
	case ev.Key() == tcell.KeyRune && ev.Rune() == '/' && focused != v.query:
		v.a.tv.SetFocus(v.query)
		return nil
	case ev.Key() == tcell.KeyRune && ev.Rune() == 'a' && focused != v.query:
		if i := v.list.GetCurrentItem(); i >= 0 && i < len(v.shown) {
			v.a.library.add(v.shown[i])
		}
		return nil
	case ev.Key() == tcell.KeyEscape && focused == v.details:
		v.a.tv.SetFocus(v.list)
		return nil
	}
	return ev
}

// search asks the backend (→ gRPC catalog) for query. Event loop only.
func (v *browserView) search(query string) {
	v.searchSeq++
	seq := v.searchSeq
	v.list.SetTitle(" Manga (searching...) ")
	v.a.setStatus(statusInfo, fmt.Sprintf("Searching %q...", query))

	api := v.a.api
	runAsync(v.a, func(ctx context.Context) ([]client.Manga, error) {
		return api.SearchManga(ctx, query)
	}, func(items []client.Manga, err error) {
		if seq != v.searchSeq {
			return // a newer search was started; drop this stale answer
		}
		if err != nil {
			v.list.SetTitle(" Manga ")
			if !v.a.sessionExpired(err) {
				v.a.setStatus(statusErr, friendlyError(err, api.BaseURL()))
			}
			return
		}
		v.results = items
		// Genre options normally come from the full catalog (empty query). If
		// that first load failed (catalog down at login), take them from the
		// first search that works instead of leaving the drop-down empty.
		if query == "" || len(v.genres) <= 1 {
			v.setGenreOptions(genresOf(items))
		}
		v.applyFilter()
		switch {
		case len(items) == 0:
			v.a.setStatus(statusWarn, fmt.Sprintf("No manga matches %q.", query))
		case len(v.shown) == 0:
			v.a.setStatus(statusWarn, fmt.Sprintf("%d result(s), none in genre %q.", len(items), v.curGenre))
		default:
			v.a.setStatus(statusOK, fmt.Sprintf("%d result(s). Up/Down to browse, / to search again.", len(v.shown)))
		}
	})
}

// setGenreOptions refills the drop-down and keeps the current choice if it
// still exists.
func (v *browserView) setGenreOptions(genres []string) {
	v.genres = append([]string{allGenres}, genres...)
	v.genre.SetOptions(v.genres, nil)
	sel := 0
	for i, g := range v.genres {
		if g == v.curGenre {
			sel = i
		}
	}
	v.genre.SetCurrentOption(sel)
	v.genre.SetSelectedFunc(func(text string, _ int) {
		if text != "" && text != v.curGenre {
			v.setGenre(text)
		}
	})
}

// setGenre filters the current results. Event loop only.
func (v *browserView) setGenre(genre string) {
	v.curGenre = genre
	v.applyFilter()
	v.a.setStatus(statusInfo, fmt.Sprintf("Genre %q: %d of %d result(s).", genre, len(v.shown), len(v.results)))
	v.a.tv.SetFocus(v.list)
}

// applyFilter rebuilds the list from results + genre and shows the first row.
func (v *browserView) applyFilter() {
	v.shown = filterByGenre(v.results, v.curGenre)
	v.rebuild = true
	v.list.Clear()
	for _, m := range v.shown {
		v.list.AddItem(listLabel(m), "", 0, nil)
	}
	v.rebuild = false
	v.list.SetTitle(fmt.Sprintf(" Manga (%d) ", len(v.shown)))
	if len(v.shown) == 0 {
		v.detailSeq++ // invalidate any details request still in flight
		v.details.SetText("[gray]No manga to show. Try another search or genre.[-]")
		return
	}
	v.list.SetCurrentItem(0)
	v.loadDetails(0)
}

// loadDetails fetches GET /api/manga/:id for row i. Event loop only.
func (v *browserView) loadDetails(i int) {
	if i < 0 || i >= len(v.shown) {
		return
	}
	m := v.shown[i]
	v.detailSeq++
	seq := v.detailSeq
	v.details.SetText(fmt.Sprintf("[::b]%s[::-]\n\n[gray]Loading details...[-]", tview.Escape(m.Title)))

	type result struct {
		m     client.Manga
		cache string
	}
	api := v.a.api
	runAsync(v.a, func(ctx context.Context) (result, error) {
		got, cache, err := api.GetManga(ctx, m.ID)
		return result{got, cache}, err
	}, func(r result, err error) {
		if seq != v.detailSeq {
			return // selection moved on
		}
		if err != nil {
			if v.a.sessionExpired(err) {
				return
			}
			msg := friendlyError(err, api.BaseURL())
			v.details.SetText(fmt.Sprintf("[::b]%s[::-]\n\n[red]%s[-]", tview.Escape(m.Title), tview.Escape(msg)))
			v.a.setStatus(statusErr, msg)
			return
		}
		v.details.SetText(detailsText(r.m, r.cache)).ScrollToBeginning()
	})
}
