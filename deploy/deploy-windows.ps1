# Раскладывает прогу (или любые exe) на рабочие столы Windows-компьютеров в локальной сети.
# Запускать с Windows: двойной щелчок по deploy-windows.cmd, или в PowerShell:
#   .\deploy\deploy-windows.ps1                              # компьютеры из hosts-windows.txt
#   .\deploy\deploy-windows.ps1 -Hosts 192.168.1.126         # только этот
#   .\deploy\deploy-windows.ps1 -Files C:\x\setup.exe -DesktopUser Vasya
# По умолчанию кладёт dist\прога узбека.exe на общий рабочий стол (C:\Users\Public\Desktop) — его видят все.
# Нужна учётка администратора на тех компьютерах (окно входа появится один раз).
param(
  [string[]]$Hosts,
  [string[]]$Files,
  [string]$DesktopUser = ""
)
$ErrorActionPreference = 'Stop'
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$root = Split-Path -Parent $here
if (-not $Files) { $Files = @(Join-Path $root 'dist\прога узбека.exe') }
foreach ($f in $Files) { if (-not (Test-Path -LiteralPath $f)) { Write-Host "Нет файла: $f" -ForegroundColor Red; exit 1 } }
if (-not $Hosts) {
  $list = Join-Path $here 'hosts-windows.txt'
  $Hosts = Get-Content -LiteralPath $list -Encoding UTF8 | ForEach-Object { ($_ -replace '#.*$', '').Trim() } | Where-Object { $_ }
}
$cred = Get-Credential -Message 'Логин и пароль администратора на тех компьютерах (для учётки Microsoft — почта и пароль, не ПИН)'
$ts = Get-Date -Format 'yyyyMMdd-HHmmss'
$report = @()

function Test-Port($h) {
  $c = New-Object Net.Sockets.TcpClient
  try { $ok = $c.ConnectAsync($h, 445).Wait(3000); return ($ok -and $c.Connected) } catch { return $false } finally { $c.Dispose() }
}

foreach ($h in $Hosts) {
  Write-Host "→ $h"
  if (-not (Test-Port $h)) {
    Write-Host '   ✗ компьютер не отвечает (выключен, не в этой сети или закрыт общий доступ к файлам)' -ForegroundColor Red
    $report += "✗ $h — не в сети"; continue
  }
  $share = "\\$h\C$"
  try {
    New-PSDrive -Name UZDEPLOY -PSProvider FileSystem -Root $share -Credential $cred -ErrorAction Stop | Out-Null
  } catch {
    Write-Host "   ✗ не пустил: $($_.Exception.Message)" -ForegroundColor Red
    Write-Host '     Проверь на том компьютере: сеть «Частная», включён «Общий доступ к файлам и принтерам», учётка — администратор.'
    Write-Host '     Для локальной учётки один раз выполни там от имени администратора:'
    Write-Host '     reg add HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System /v LocalAccountTokenFilterPolicy /t REG_DWORD /d 1 /f'
    $report += "✗ $h — нет доступа"; continue
  }
  $ok = $true
  try {
    $pub = 'UZDEPLOY:\Users\Public'
    $dest = "$pub\Desktop"
    if ($DesktopUser) {
      $dest = "UZDEPLOY:\Users\$DesktopUser\Desktop"
      foreach ($od in @("UZDEPLOY:\Users\$DesktopUser\OneDrive\Desktop", "UZDEPLOY:\Users\$DesktopUser\OneDrive\Рабочий стол")) { if (Test-Path -LiteralPath $od) { $dest = $od } }
    }
    if (-not (Test-Path -LiteralPath $dest)) { throw "нет папки $($dest -replace '^UZDEPLOY:', 'C:')" }
    foreach ($f in $Files) {
      $name = Split-Path -Leaf $f
      $part = Join-Path $dest ".$name.part"
      Copy-Item -LiteralPath $f -Destination $part -Force
      $target = Join-Path $dest $name
      if (Test-Path -LiteralPath $target) {
        # запущенный exe нельзя перезаписать, но можно переименовать — убираем его с рабочего стола
        Move-Item -LiteralPath $target -Destination (Join-Path $pub "$name.old-$ts") -Force
        Get-ChildItem -LiteralPath $pub -Filter "$name.old-*" | Remove-Item -Force -ErrorAction SilentlyContinue
      }
      Move-Item -LiteralPath $part -Destination $target -Force
      Write-Host "   ✓ $name → $($dest -replace '^UZDEPLOY:', 'C:')" -ForegroundColor Green
    }
  } catch {
    Write-Host "   ✗ $($_.Exception.Message)" -ForegroundColor Red; $ok = $false
  } finally {
    Remove-PSDrive -Name UZDEPLOY -ErrorAction SilentlyContinue
  }
  if ($ok) { $report += "✓ $h" } else { $report += "✗ $h — не получилось" }
}
Write-Host ''
Write-Host 'Итого:'
$report | ForEach-Object { Write-Host "  $_" }
