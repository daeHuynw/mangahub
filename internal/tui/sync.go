package tui

import (
	"context"
	"fmt"
	"sync/atomic"

	"mangahub/internal/tui/client"
	"mangahub/internal/tui/tcpclient"
)

// syncState is what the UI knows about the TCP progress-sync session.
// Event loop only.
type syncState struct {
	state     tcpclient.State
	sessionID string
	lastErr   string
	connects  int                // successful auth_ok count (>1 means we reconnected)
	gen       int                // bumped on every start/stop; drops callbacks of an old session
	cancel    context.CancelFunc // stops the running tcpclient.Run
	active    *atomic.Bool       // false once this session was stopped (read from its goroutine)
}

// startSync opens the TCP session after login. Event loop only.
func (a *App) startSync() {
	a.stopSync()
	a.sync.gen++
	gen := a.sync.gen
	ctx, cancel := context.WithCancel(context.Background())
	active := &atomic.Bool{}
	active.Store(true)
	a.sync.cancel, a.sync.active = cancel, active
	a.sync.state = tcpclient.Connecting

	cfg := tcpclient.Config{Addr: a.cfg.TCP, Token: a.api.Token}
	h := tcpclient.Handler{
		OnState: func(s tcpclient.State, sid string, err error) {
			// Set the session id right here, not in the UI callback: the next
			// HTTP PUT must already carry X-Session-ID so the backend does not
			// echo our own change back to us. A stopped session must not touch
			// it any more: after logout + quick login its late "Stopped" would
			// otherwise wipe the new session's id.
			if active.Load() {
				if s == tcpclient.Connected {
					a.api.SetSessionID(sid)
				} else {
					a.api.SetSessionID("")
				}
			}
			a.post(func() {
				if gen == a.sync.gen {
					a.onSyncState(s, sid, err)
				}
			})
		},
		OnProgress: func(p client.Progress) {
			a.post(func() {
				if gen == a.sync.gen {
					a.library.applyRemote(p)
				}
			})
		},
	}
	go tcpclient.Run(ctx, cfg, h)
}

// stopSync closes the TCP session (logout, expired token, quit).
func (a *App) stopSync() {
	if a.sync.active != nil {
		a.sync.active.Store(false)
		a.sync.active = nil
	}
	if a.sync.cancel != nil {
		a.sync.cancel()
		a.sync.cancel = nil
	}
	a.sync.gen++
	a.sync.state = tcpclient.Stopped
	a.sync.sessionID = ""
	a.sync.connects = 0
	a.api.SetSessionID("")
}

// onSyncState updates the indicator and, after a reconnect, reloads the
// library: events sent while we were offline are lost (the server does not
// queue them), so the only way to catch up is to ask HTTP again.
func (a *App) onSyncState(s tcpclient.State, sid string, err error) {
	prev := a.sync.state
	a.sync.state, a.sync.sessionID = s, sid
	if err != nil {
		a.sync.lastErr = err.Error()
	}
	switch s {
	case tcpclient.Connected:
		a.sync.connects++
		if a.sync.connects > 1 {
			a.setStatus(statusOK, fmt.Sprintf("Progress sync reconnected (session %s). Reloading your library...", sid))
			if a.library.loaded {
				a.library.reload()
			}
		}
	case tcpclient.Disconnected:
		if prev == tcpclient.Connected { // say it once, not on every retry
			a.setStatus(statusWarn, "Progress sync lost. Reconnecting in the background...")
		}
	case tcpclient.AuthFailed:
		a.setStatus(statusErr, "Progress sync: the server rejected the token. Log in again to resume.")
	}
	a.refreshHeader()
	if name, _ := a.pages.GetFrontPage(); name == pageHelp {
		a.help.render()
	}
}

// syncLabel is the short indicator shown in the header.
func (a *App) syncLabel() string {
	switch a.sync.state {
	case tcpclient.Connected:
		return "[green]sync on[-]"
	case tcpclient.Connecting:
		return "[yellow]sync connecting[-]"
	case tcpclient.Disconnected:
		return "[red]sync off[-]"
	case tcpclient.AuthFailed:
		return "[red]sync rejected[-]"
	}
	return "[gray]sync off[-]"
}
