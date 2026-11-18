# AI-assisted work log

What we asked an AI assistant (Claude Code) to do, what it got wrong, and how we checked it.
REPORT section 4 needs three of these, including at least one verification/review decision.
Entries below are member B's; member A adds their own.

## B-1. Tests that hung: `t.Fatal` inside the tview event loop (B4)

The AI wrote UI tests that read widget state through `app.QueueUpdate(func() { ... })`. One test
called `t.Fatalf` inside that callback. When the assertion failed (a timing issue under `-race`),
`t.Fatalf` ran `runtime.Goexit` on the tview goroutine, the event loop died and the next
`QueueUpdate` waited forever. The run hung until the 10 min `go test` timeout.

How we found it: re-ran with `-timeout 60s` to get a goroutine dump; the tview `Run` goroutine was
gone and the test was blocked in `QueueUpdate`. Fix: read state inside the callback, assert outside
it, and give the test helper a 5 s timeout so a dead event loop fails fast instead of hanging. The
same test also passed too early because it checked text that was already on screen before the key
presses were handled; it now waits for the request sequence number.

## B-2. Logout did not unsubscribe from UDP alerts (B6, defect D3)

The AI's first `udpclient` read the JWT when building the `unsubscribe` datagram. The logout code
cancels the UDP client and clears the token right after, so the datagram went out with an empty
token, the server answered "invalid token" and kept sending alerts to the old port for 90 s. Quit
worked only because the token was still set at that moment, so manual testing would likely have
missed it.

Caught by a UI test the AI wrote (`TestUI_LogoutUnsubscribes`) against A's real notifier. Fix:
unsubscribe reuses the token from the last subscribe; added a regression test that clears the token
before cancelling.

## B-3. Verification decision: first catalog call after backend start (defect D1)

During a live run the first request after starting the backend returned 503 after exactly 2 s.
The AI's first guess was a Windows firewall prompt for the new binary. We did not accept that
without evidence: restarted the backend three times (always reproducible), turned on grpc-go logs
(`GRPC_GO_LOG_SEVERITY_LEVEL=info`) and saw 2 s between channel creation and "Resolver state
updated", then timed the DNS TXT query for `_grpc_config.localhost` (~2.9 s, timeout). Measured both
fixes before writing them down (IP address: 3 ms, TXT lookup off: 10 ms). The code is A's, so we
reported it in `notes/defects.md` instead of changing it.

## B-4. Smaller corrections

- Quick `+` presses in My Library sent the same chapter twice (`[1 2 3 3 3]` for five presses).
  The AI's first version computed "+1" from the displayed chapter without waiting for the previous
  answer. Fixed with one in-flight request per manga; the new test fails on the old code.
- The AI committed its working-notes file (`CLAUDE.md`) on the first feature branch. We noticed
  before the first push and rebased it out of the branch history; it stays local only.
- Commit messages were first written in unaccented Vietnamese; redone in English to match the repo
  and the assignment's language rule before pushing.
- The AI warned that `go test -race` might not work with the 32-bit MinGW gcc on this machine. We
  checked `go env CC` (64-bit w64devkit) and ran it: it works.
- In the chat client the AI used two separate `defer`s: "close the stop channel" and "wait for the
  writer goroutine". Defers run in reverse order, so it waited for the writer, which was waiting for
  `stop`: `Run` never returned after cancel. Every wsclient test that cancelled timed out; fixed by
  closing and waiting in one defer.
- The AI's Discussion screen scrolled the message view from `TextView.SetChangedFunc`. tview calls that
  callback from its own goroutine, so it touched a widget outside the event loop. `go test -race` caught
  it on the first run; removed (render() already scrolls inside the event loop).

