<#
.SYNOPSIS
    Installs (or updates) AyrPlay for the current user.

.DESCRIPTION
    Downloads AyrPlay.exe from the project's GitHub Releases (built by CI from
    the tagged source), verifies it against the release's SHA256SUMS.txt,
    installs GStreamer with winget if it is missing, adds a Start menu shortcut
    and an `ayrplay` command, and starts the tray app. No administrator rights
    are needed. Read this script before running it; it only touches
    %LOCALAPPDATA%\Programs\AyrPlay, your Start menu, and your user PATH.

.EXAMPLE
    irm https://raw.githubusercontent.com/ayaanh03/AyrPlay/main/scripts/install.ps1 | iex

.EXAMPLE
    # Pin a version, skip GStreamer, don't launch (for scripts/agents):
    & ([scriptblock]::Create((irm https://raw.githubusercontent.com/ayaanh03/AyrPlay/main/scripts/install.ps1))) -Version v1.0.0 -NoGStreamer -NoLaunch
#>
param(
    # Release tag to install, e.g. v1.0.0. Defaults to the latest release.
    [string]$Version = 'latest',
    # Don't install GStreamer even if it is missing.
    [switch]$NoGStreamer,
    # Don't add the Start menu shortcut.
    [switch]$NoShortcut,
    # Don't add the install folder to the user PATH.
    [switch]$NoPath,
    # Don't start the tray app when done.
    [switch]$NoLaunch,
    # Remove AyrPlay, its shortcut, and its PATH entry instead of installing.
    [switch]$Uninstall
)

# Run in a child scope so `irm ... | iex` leaves the caller's session settings alone.
& {
    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue' # Invoke-WebRequest is very slow with a progress bar
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    $Repo = 'ayaanh03/AyrPlay'
    $InstallDir = Join-Path $env:LOCALAPPDATA 'Programs\AyrPlay'
    $Exe = Join-Path $InstallDir 'AyrPlay.exe'
    $GStreamerWingetId = 'gstreamerproject.gstreamer'
    $GStreamerDownload = 'https://gstreamer.freedesktop.org/download/'

    function Get-ShortcutPath {
        return Join-Path ([Environment]::GetFolderPath('Programs')) 'AyrPlay.lnk'
    }

    function Stop-AyrPlay {
        Get-Process -Name 'AyrPlay' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    }

    function Get-UserPath {
        $value = [Environment]::GetEnvironmentVariable('Path', 'User')
        if ($null -eq $value) { return @() }
        return @($value -split ';' | Where-Object { $_ -ne '' })
    }

    function Set-UserPath([string[]]$Entries) {
        [Environment]::SetEnvironmentVariable('Path', ($Entries -join ';'), 'User')
    }

    if ($Uninstall) {
        Stop-AyrPlay
        Remove-Item -LiteralPath (Get-ShortcutPath) -Force -ErrorAction SilentlyContinue
        Set-UserPath (Get-UserPath | Where-Object { $_ -ne $InstallDir })
        Remove-Item -LiteralPath $InstallDir -Recurse -Force -ErrorAction SilentlyContinue
        Write-Host 'AyrPlay removed. Settings and logs in %AppData%\doubletake were left in place.'
        return
    }

    # 1. Download and verify AyrPlay.exe.
    $asset = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'AyrPlay-arm64.exe' } else { 'AyrPlay.exe' }
    $base = if ($Version -eq 'latest') {
        "https://github.com/$Repo/releases/latest/download"
    } else {
        "https://github.com/$Repo/releases/download/$Version"
    }
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $download = Join-Path $InstallDir 'AyrPlay.exe.download'
    $sums = Join-Path $InstallDir 'SHA256SUMS.txt'
    Write-Host "Downloading $asset ($Version)..."
    Invoke-WebRequest -Uri "$base/$asset" -OutFile $download -UseBasicParsing
    Invoke-WebRequest -Uri "$base/SHA256SUMS.txt" -OutFile $sums -UseBasicParsing

    $pattern = '^([0-9a-fA-F]{64})\s+\*?' + [regex]::Escape($asset) + '$'
    $expected = Get-Content -LiteralPath $sums | ForEach-Object {
        if ($_ -match $pattern) { $Matches[1] }
    } | Select-Object -First 1
    $actual = (Get-FileHash -LiteralPath $download -Algorithm SHA256).Hash
    if (-not $expected -or $actual -ne $expected) {
        Remove-Item -LiteralPath $download -Force
        throw "Checksum mismatch for $asset (expected '$expected', got '$actual'). Nothing was installed."
    }
    Write-Host "Verified SHA-256 $actual"

    Stop-AyrPlay
    Move-Item -LiteralPath $download -Destination $Exe -Force
    Remove-Item -LiteralPath $sums -Force

    # 2. Make `ayrplay` available in new terminals (for people and agents).
    if (-not $NoPath) {
        $entries = Get-UserPath
        if ($entries -notcontains $InstallDir) {
            Set-UserPath ($entries + $InstallDir)
            Write-Host "Added $InstallDir to your user PATH (new terminals will see it)."
        }
    }

    # 3. Start menu shortcut.
    if (-not $NoShortcut) {
        $shell = New-Object -ComObject WScript.Shell
        $link = $shell.CreateShortcut((Get-ShortcutPath))
        $link.TargetPath = $Exe
        $link.WorkingDirectory = $InstallDir
        $link.Description = 'AyrPlay - mirror your screen or stream audio over AirPlay 2'
        $link.Save()
    }

    # 4. GStreamer (AyrPlay finds it after install without any PATH changes).
    function Test-AyrPlayReady {
        & $Exe doctor | Out-Host
        return ($LASTEXITCODE -eq 0)
    }
    $ready = Test-AyrPlayReady
    if (-not $ready -and -not $NoGStreamer) {
        if (Get-Command winget -ErrorAction SilentlyContinue) {
            Write-Host "Installing GStreamer with winget ($GStreamerWingetId)..."
            winget install --id $GStreamerWingetId --exact --accept-package-agreements --accept-source-agreements
            $ready = Test-AyrPlayReady
        }
        if (-not $ready) {
            Write-Warning "GStreamer is missing or incomplete. Install the MSVC 64-bit runtime from $GStreamerDownload, then run 'ayrplay doctor' to check."
            Start-Process $GStreamerDownload
        }
    }

    Write-Host ''
    Write-Host "AyrPlay installed to $Exe"
    Write-Host "If Windows Firewall asks, allow AyrPlay on Private networks so it can find your Apple TV / HomePod."
    if (-not $NoLaunch) {
        Start-Process -FilePath $Exe
        Write-Host 'AyrPlay is running in the system tray (click ^ near the clock if you do not see it).'
    }
}
