# Install the latest claudesync release into %LOCALAPPDATA%\Programs\claudesync
# and add it to the user PATH.
$ErrorActionPreference = "Stop"
$repo = "Hudint/claudesync"
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
$dir = Join-Path $env:LOCALAPPDATA "Programs\claudesync"
$url = "https://github.com/$repo/releases/latest/download/claudesync_windows_$arch.zip"
$zip = Join-Path $env:TEMP "claudesync.zip"
Write-Host "downloading $url"
Invoke-WebRequest -Uri $url -OutFile $zip -UseBasicParsing
New-Item -ItemType Directory -Force -Path $dir | Out-Null
Expand-Archive -Path $zip -DestinationPath $dir -Force
Remove-Item $zip
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (($userPath -split ";") -notcontains $dir) {
  [Environment]::SetEnvironmentVariable("Path", "$userPath;$dir", "User")
  Write-Host "added $dir to your PATH"
}
# Make claudesync usable in this window too, not only in new ones.
if (($env:Path -split ";") -notcontains $dir) { $env:Path += ";$dir" }
Write-Host "installed $dir\claudesync.exe"
& (Join-Path $dir "claudesync.exe") version
Write-Host "restart Claude Code so its hooks find claudesync"
