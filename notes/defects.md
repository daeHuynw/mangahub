# Defects found while testing

Format: symptom → cause → fix → retest. Candidates for REPORT section 4.

## D1. First catalog request after backend start fails with 503 (Windows)

Found by: member B, 2026-11-18, during the B4 end-to-end run. Owner of the code path: member A
(`internal/api/catalog_client.go`, `CATALOG_ADDR`). **Status: open, needs A's decision.**

**Symptom.** Right after `cmd/backend` starts, the first request that needs the catalog returns
503 after exactly 2 s (`DeadlineExceeded`). The next request works in ~2 ms. Reproduced 3 times
in a row by restarting the backend and calling `GET /api/manga/naruto`:

```
503 2.002760s   <- first call after start
200 0.029260s
200 0.002108s
```

The catalog log shows no `rpc ...` line for the failed call, so the request never reached it.

**Cause.** With `GRPC_GO_LOG_SEVERITY_LEVEL=info` the backend log shows the channel created at
20:16:12 but `Resolver state updated` only at 20:16:14; the TCP connect to `[::1]:9092` after that
is instant. `CATALOG_ADDR=localhost:9092` goes through grpc-go's `dns` resolver, which also asks
DNS for the TXT record `_grpc_config.localhost` (service config) and waits for that answer before
handing out addresses. On this network that TXT query times out:

```
> Resolve-DnsName -Type TXT _grpc_config.localhost   -> ~2950 ms, "DNS request timed out"
```

So the first RPC spends its whole 2 s deadline waiting for name resolution. The unit tests did not
catch it because they all dial `127.0.0.1:<port>`.

**Fix options (not applied, A's code/config):**

| Option | Change | First call measured |
| --- | --- | --- |
| a | `CATALOG_ADDR=127.0.0.1:9092` (IP literal, no DNS) | 200 in 3 ms |
| b | `GRPC_ENABLE_TXT_SERVICE_CONFIG=false` (env only) | 200 in 10 ms |
| c | `grpc.WithDisableServiceConfig()` in `DialCatalog` (skips the TXT lookup in code) | not measured yet |
| d | call `conn.Connect()` at startup so resolution happens before the first user request | not measured yet |

Suggested: (c) in code, plus (a) as the default in `.env.example` / contracts. Retest: restart the
backend, first `GET /api/manga/:id` must be 200 well under 2 s.

## D2. Update Chapter dialog: after an error the cursor stayed on "Save"

Found by: member B, 2026-11-18, by the B4 UI test. **Status: fixed in B4.**

**Symptom.** Typing a chapter out of range and pressing Save showed the error, but the next keys
went to the Save button, so the user had to Tab back to the field to fix the number.

**Cause.** The form keeps focus where the user left it (the button).

**Fix.** `libraryView.formError` shows the message and moves focus back to the chapter field, for
both the local range check and a 400 from the backend.

**Retest.** `TestUI_LibraryAddEditAndValidate` types 9999, gets the error, then types 13 directly
and sees the backend's 400 message (passes with `-race`).

## D3. Logout left the UDP subscription alive for 90 s

Found by: member B, 2026-11-18, by `TestUI_LogoutUnsubscribes`. **Status: fixed in B6.**

**Symptom.** After Ctrl+L the server still listed the TUI's address and a release was sent to it
(quitting with Ctrl+C was fine).

**Cause.** `endSession` cancels the UDP client and then calls `api.Logout()`, which clears the JWT.
The client goroutine read the token only when building the `unsubscribe` datagram, i.e. after it was
cleared, so it sent an empty token; the notifier answered "invalid token" and kept the subscriber
until its 90 s TTL. Quit worked only because the token was still set at that moment: a timing bug.

**Fix.** `udpclient.Run` keeps the token of the last subscribe and uses it for unsubscribe.

**Retest.** `TestUnsubscribeAfterTokenCleared` (token cleared before cancel) and the UI logout test both
pass; live run: `udpnotify: unsubscribe 127.0.0.1:52936` right after C's logout.

## D4. Two quick "+" presses sent the same chapter twice

Found by: member B, 2026-11-18, code review of B4. **Status: fixed.**

**Symptom.** Five fast "+" presses on one row produced PUTs `[1 2 3 3 3]`: some presses were lost and
the server got duplicate updates (each still bumped the version).

**Cause.** Each press computed `current_chapter + 1` from the row on screen. Until the previous PUT
answered, the row still showed the old chapter, so several presses sent the same value.

**Fix.** One progress request per manga at a time (`libraryView.inFlight`); a press during a save shows
"Still saving the previous change...".

**Retest.** `TestUI_QuickPlusDoesNotSendDuplicates`: five presses give `[1 2 3]`, no duplicates, and
the row matches the last PUT. With the fix removed the same test fails with `chapter 3 sent twice`.

## D5. TCP auth line without `\n` is accepted after the auth timeout (flaky test)

Found by: member B, 2026-11-18, while running the full `go test -race ./...`. Owner: member A
(`internal/tcpsync/tcpsync.go`, `handle`). **Status: open, needs A's fix.**

**Symptom.** `TestMissingNewlineTimesOut` failed once in a full parallel run ("got a reply without a
newline-terminated auth line") and passed 5/5 when run alone. The server log of a *passing* run still
shows the session being created right at the 5 s timeout:

```
20:53:47 tcpsync: listening on 127.0.0.1:60942
20:53:52 tcpsync: user 1 +s1 (1 session(s))   <- authenticated from a line with no `\n`
20:53:52 tcpsync: user 1 -s1
```

**Cause.** When the read deadline expires, `conn.Read` returns an error while the bytes
`{"type":"auth",...}` are still in the scanner's buffer. `bufio.Scanner` then calls the split function
with `atEOF=true`, and `bufio.ScanLines` returns the leftover bytes as a final token even though there
is no newline. `sc.Scan()` returns true, the server parses the JSON and authenticates. The next `Scan`
fails at once, so the session is removed; whether `auth_ok` reaches the client before the socket closes
is a race, hence the flaky test. The framing rule "one JSON object per line" is not enforced for the
first message.

**Fix (suggested, A's code).** After the first `sc.Scan()`, also reject when `sc.Err() != nil` (the
scanner keeps the read error even when it hands out that final token), or use a split function that
never returns a token without `\n`. Retest: the existing test, plus checking that the log has no
`+s1` line.

