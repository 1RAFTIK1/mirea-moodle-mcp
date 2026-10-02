# Установка mirea-moodle-mcp (Windows, PowerShell):
#   irm https://raw.githubusercontent.com/1RAFTIK1/mirea-moodle-mcp/main/install.ps1 | iex
$ErrorActionPreference = "Stop"
$repo = "1RAFTIK1/mirea-moodle-mcp"
$dir  = Join-Path $env:LOCALAPPDATA "mirea-moodle-mcp"
$exe  = Join-Path $dir "mirea-moodle-mcp.exe"
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$url = "https://github.com/$repo/releases/latest/download/mirea-moodle-mcp-windows-$arch.exe"
Write-Host "→ Скачиваю $url"
$tmp = "$exe.tmp"
Invoke-WebRequest -Uri $url -OutFile $tmp -UseBasicParsing
$sums = (Invoke-WebRequest -Uri "https://github.com/$repo/releases/latest/download/SHA256SUMS" -UseBasicParsing).Content
if ($sums -is [byte[]]) { $sums = [Text.Encoding]::UTF8.GetString($sums) }
$name = "mirea-moodle-mcp-windows-$arch.exe"
$want = ($sums -split "`n" | Where-Object { $_ -match "\s\*?$([regex]::Escape($name))\s*$" } | ForEach-Object { ($_ -split '\s+')[0] }) | Select-Object -First 1
$got = (Get-FileHash -Algorithm SHA256 $tmp).Hash.ToLower()
if (-not $want -or $want.ToLower() -ne $got) {
  Remove-Item $tmp -Force
  throw "Контрольная сумма не совпала (ожидалась $want, получена $got). Установка прервана."
}
Write-Host "✓ SHA-256 совпадает"
Move-Item -Force $tmp $exe
Write-Host "✓ Установлено: $exe"
$p = [Environment]::GetEnvironmentVariable("Path", "User")
if ($p -notlike "*$dir*") {
  [Environment]::SetEnvironmentVariable("Path", "$p;$dir", "User")
  Write-Host "  (папка добавлена в PATH — новые окна терминала увидят команду mirea-moodle-mcp)"
}
& $exe setup
