# Proposed additions for README.md and .env.example (for A to paste)

B does not edit these files. Copy what you agree with.

## README.md

### Add to the Configuration table

| Variable | Default | Used by |
| --- | --- | --- |
| `CATALOG_SEED` | empty = seed built into the binary | catalog (path to another seed JSON; read error stops the catalog) |
| `MANGAHUB_LOG` | `tui.log` | TUI (log file; the terminal is used by the UI) |

### New section after "Run"

```markdown
## Using the TUI

Log in or register on the first screen (Tab moves, Enter confirms). After login the TUI opens a
TCP session, subscribes to UDP alerts and opens the chat WebSocket; the header shows
`sync on`, `alerts on`, `chat on`.

| Key | Where | Action |
| --- | --- | --- |
| F2 | anywhere | Manga Browser (search, genre filter, details) |
| F3 | anywhere | My Library |
| F4 | anywhere | Discussion (room chat) |
| F5 | anywhere | Notifications (new-chapter alerts) |
| F6 | anywhere | Connection status / help |
| `/` | browser | focus the search box |
| `a` | browser | add the selected manga to the library |
| Enter | library | edit chapter and status |
| `+` / `-` | library | one chapter up / down |
| `r` | library | reload from the backend |
| `/join <room>` | discussion | switch room (or Enter on a room in the list) |
| Ctrl+L | anywhere | log out (closes TCP, unsubscribes UDP, closes chat) |
| Ctrl+C | anywhere | quit |
```

### Add to "Known limitations"

```markdown
- Chat keeps no history: a TUI only sees messages sent while it is connected.
- On some Windows networks the first catalog call after the backend starts can time out (503),
  see notes/defects.md D1. Workaround: `CATALOG_ADDR=127.0.0.1:9092`.
```

### Windows demo

```markdown
On Windows: `powershell -ExecutionPolicy Bypass -File scripts/demo.ps1` (same steps as demo.sh;
there is no SIGSTOP on Windows, so the frozen-catalog case is shown with the test instead).
```

## .env.example

Add under `# Catalog service (cmd/catalog)`:

```
# Optional: path to another seed JSON (default: the one built into the binary)
CATALOG_SEED=
```

Add under `# TUI (cmd/tui)`:

```
# Log file; the terminal itself is used by the UI
MANGAHUB_LOG=tui.log
```

If D1 is not fixed in code, also consider `CATALOG_ADDR=127.0.0.1:9092` as the default.
