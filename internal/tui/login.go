package tui

import (
	"context"
	"net/http"

	"github.com/rivo/tview"

	"mangahub/internal/tui/client"
)

// loginView is the Login / Register screen.
type loginView struct {
	a        *App
	root     tview.Primitive
	form     *tview.Form
	username *tview.InputField
	password *tview.InputField
	message  *tview.TextView
	busy     bool // a request is in flight: ignore repeated Enter presses
}

func newLoginView(a *App) *loginView {
	v := &loginView{a: a, form: tview.NewForm(), message: tview.NewTextView().SetDynamicColors(true)}
	v.username = tview.NewInputField().SetLabel("Username").SetFieldWidth(32)
	v.password = tview.NewInputField().SetLabel("Password").SetFieldWidth(32).SetMaskCharacter('*')
	v.form.AddFormItem(v.username).AddFormItem(v.password).
		AddButton("Login", func() { v.submit(false) }).
		AddButton("Register", func() { v.submit(true) }).
		AddButton("Quit", a.Stop)
	v.form.SetBorder(true).SetTitle(" MangaHub - Login / Register ")

	// Center a fixed-size box: spacer | (spacer / form / message / spacer) | spacer.
	box := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(nil, 0, 1, false).
		AddItem(v.form, 9, 0, true).
		AddItem(v.message, 2, 0, false).
		AddItem(nil, 0, 1, false)
	v.root = tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(box, 50, 0, true).
		AddItem(nil, 0, 1, false)
	return v
}

func (v *loginView) show(color, msg string) {
	v.message.SetText("[" + color + "]" + tview.Escape(msg) + "[-]")
}

// submit validates locally, then logs in (or registers and logs in) in the
// background. Runs in the event loop.
func (v *loginView) submit(register bool) {
	if v.busy {
		return
	}
	user, pass := v.username.GetText(), v.password.GetText()
	if msg := validateCredentials(user, pass); msg != "" {
		v.show("red", msg)
		return
	}
	v.busy = true
	if register {
		v.show("yellow", "Creating account...")
	} else {
		v.show("yellow", "Logging in...")
	}

	api := v.a.api
	runAsync(v.a, func(ctx context.Context) (client.User, error) {
		if register {
			if _, err := api.Register(ctx, user, pass); err != nil {
				return client.User{}, err
			}
		}
		return api.Login(ctx, user, pass)
	}, func(u client.User, err error) {
		v.busy = false
		if err != nil {
			msg := friendlyError(err, api.BaseURL())
			if client.StatusOf(err) == http.StatusUnauthorized {
				msg = "Wrong username or password."
			}
			v.show("red", msg)
			v.a.setStatus(statusErr, msg)
			return
		}
		v.password.SetText("")
		v.show("green", "Logged in.")
		v.a.onLoggedIn(u)
	})
}
