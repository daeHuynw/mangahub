# Fresh demo environment on Windows (same steps as scripts/demo.sh):
#   1. delete old databases
#   2. build and start the catalog (gRPC) and the backend
#   3. create demo accounts alice / bob / charlie (password secret123)
#   4. print the commands for the TUI windows, the chapter release and the catalog fault demo
# Run from any folder:
#   powershell -ExecutionPolicy Bypass -File scripts\demo.ps1
# Ctrl+C stops catalog and backend.

$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

function Default-Env($name, $value) {
    if (-not [Environment]::GetEnvironmentVariable($name)) {
        [Environment]::SetEnvironmentVariable($name, $value, 'Process')
    }
}
Default-Env 'HTTP_ADDR' ':8080'
Default-Env 'ADMIN_KEY' 'dev-admin-key'
Default-Env 'JWT_SECRET' ("demo-secret-" + [DateTimeOffset]::Now.ToUnixTimeSeconds())
# IP instead of "localhost": avoids the slow DNS TXT lookup in grpc-go on Windows
# (notes/defects.md D1), which can make the first catalog call return 503.
Default-Env 'CATALOG_ADDR' '127.0.0.1:9092'
$base = if ($env:MANGAHUB_HTTP) { $env:MANGAHUB_HTTP } else { 'http://localhost:8080' }

Write-Host '== 1. clean databases'
Remove-Item -Force -ErrorAction SilentlyContinue backend.db, backend.db-shm, backend.db-wal, catalog.db, catalog.db-shm, catalog.db-wal

Write-Host '== 2. build + start'
New-Item -ItemType Directory -Force bin | Out-Null
go build -o bin\catalog.exe .\cmd\catalog
if ($LASTEXITCODE -ne 0) { throw 'build catalog failed' }
go build -o bin\backend.exe .\cmd\backend
if ($LASTEXITCODE -ne 0) { throw 'build backend failed' }
go build -o bin\tui.exe .\cmd\tui
if ($LASTEXITCODE -ne 0) { throw 'build tui failed' }

# Go's log package writes to stderr -> bin\*.log (sync, alerts, chat, rpc lines);
# Gin's request log goes to stdout -> bin\*.out.
$catalog = Start-Process -FilePath bin\catalog.exe -NoNewWindow -PassThru `
    -RedirectStandardOutput bin\catalog.out -RedirectStandardError bin\catalog.log
$backend = Start-Process -FilePath bin\backend.exe -NoNewWindow -PassThru `
    -RedirectStandardOutput bin\backend.out -RedirectStandardError bin\backend.log

try {
    $up = $false
    for ($i = 0; $i -lt 50; $i++) {
        try { Invoke-RestMethod -TimeoutSec 1 "$base/api/health" | Out-Null; $up = $true; break }
        catch { Start-Sleep -Milliseconds 200 }
    }
    if (-not $up) { throw 'backend did not start, see bin\backend.log' }
    Write-Host "   catalog pid $($catalog.Id) (log bin\catalog.log), backend pid $($backend.Id) (log bin\backend.log)"

    Write-Host '== 3. demo accounts'
    foreach ($u in 'alice', 'bob', 'charlie') {
        $body = @{ username = $u; password = 'secret123' } | ConvertTo-Json
        try {
            Invoke-RestMethod -Method Post "$base/api/auth/register" -ContentType 'application/json' -Body $body | Out-Null
            Write-Host "   $u / secret123 -> 201"
        } catch {
            Write-Host "   $u / secret123 -> $($_.Exception.Response.StatusCode.value__)"
        }
    }
    # Warm up the backend -> catalog connection so the first demo search is not the slow one.
    $login = Invoke-RestMethod -Method Post "$base/api/auth/login" -ContentType 'application/json' `
        -Body (@{ username = 'alice'; password = 'secret123' } | ConvertTo-Json)
    try { Invoke-RestMethod -TimeoutSec 5 -Headers @{ Authorization = "Bearer $($login.token)" } "$base/api/manga/naruto" | Out-Null } catch {}

    Write-Host @"

== 4. ready. In other terminals (cmd or PowerShell, not Git Bash):
   .\bin\tui.exe          # TUI #1 (alice)
   .\bin\tui.exe          # TUI #2 (alice again -> TCP sync)
   .\bin\tui.exe          # TUI #3 (bob / charlie -> isolation, other chat room)

   Chapter release (UDP, F04):
   curl.exe -X POST $base/api/admin/manga/one-piece/release -H "X-Admin-Key: $env:ADMIN_KEY" -H "Content-Type: application/json" -d '{\"total_chapters\": 1131}'

   gRPC failure demo:
   Stop-Process -Id $($catalog.Id)        # catalog gone -> search shows 503 at once
   Start-Process -NoNewWindow bin\catalog.exe   # back -> works again, backend not restarted
   (No SIGSTOP on Windows: show the frozen-catalog case with
    go test -run ClientDeadlineReachesServer -v ./internal/catalog/)

Press Ctrl+C to stop catalog + backend.
"@
    while ($true) {
        Start-Sleep -Seconds 1
        if ($backend.HasExited) { Write-Host 'backend exited, see bin\backend.log'; break }
    }
}
finally {
    Write-Host "`nstopping"
    foreach ($p in $backend, $catalog) {
        if ($p -and -not $p.HasExited) { Stop-Process -Id $p.Id -ErrorAction SilentlyContinue }
    }
}
