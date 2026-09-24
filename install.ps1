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
Invoke-WebRequest -Uri $url -OutFile $exe -UseBasicParsing
Write-Host "✓ Установлено: $exe"
$p = [Environment]::GetEnvironmentVariable("Path", "User")
if ($p -notlike "*$dir*") {
  [Environment]::SetEnvironmentVariable("Path", "$p;$dir", "User")
  Write-Host "  (папка добавлена в PATH — новые окна терминала увидят команду mirea-moodle-mcp)"
}
& $exe setup
