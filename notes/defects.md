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
