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
    Works in Windows PowerShell 5.1 and PowerShell 7.

.EXAMPLE
    irm https://raw.githubusercontent.com/ayaanh03/AyrPlay/main/scripts/install.ps1 | iex

.EXAMPLE
    # Pin a version, skip GStreamer, don't launch (for scripts/agents):
    & ([scriptblock]::Create((irm https://raw.githubusercontent.com/ayaanh03/AyrPlay/main/scripts/install.ps1))) -Version v1.0.0 -NoGStreamer -NoLaunch

.EXAMPLE
    # Offline: install from a folder holding AyrPlay.exe and SHA256SUMS.txt.
    .\install.ps1 -AssetDir C:\Downloads\AyrPlay
#>
param(
    # Release tag to install, e.g. v1.0.0. Defaults to the latest release.
    [string]$Version = 'latest',
    # Install from a local folder holding AyrPlay.exe (or AyrPlay-arm64.exe) and
    # SHA256SUMS.txt instead of downloading them.
    [string]$AssetDir,
    # GitHub repository to install from (owner/name), e.g. for a fork.
    [string]$Repo = 'ayaanh03/AyrPlay',
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

    # GitHub requires TLS 1.2. Leave SystemDefault (0) alone so the OS can
    # negotiate; only add TLS 1.2 to an explicit protocol list that lacks it.
    $tls12 = 3072
    $protocols = [int][Net.ServicePointManager]::SecurityProtocol
    if ($protocols -ne 0 -and ($protocols -band $tls12) -eq 0) {
        [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]($protocols -bor $tls12)
    }

    $InstallDir = Join-Path $env:LOCALAPPDATA 'Programs\AyrPlay'
    $Exe = Join-Path $InstallDir 'AyrPlay.exe'
    $UserAgent = 'AyrPlay-installer'
    $GStreamerWingetId = 'gstreamerproject.gstreamer'
    $GStreamerDownload = 'https://gstreamer.freedesktop.org/download/'
    $ReleasesPage = "https://github.com/$Repo/releases"
    $BuildFromSource = "https://github.com/$Repo#build-from-source"

    function Get-ShortcutPath {
        return Join-Path ([Environment]::GetFolderPath('Programs')) 'AyrPlay.lnk'
    }

    function Stop-AyrPlay {
        Get-Process -Name 'AyrPlay' -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    }

    # The user PATH is read and written in the registry directly so entries
    # like %USERPROFILE%\... stay unexpanded (REG_EXPAND_SZ);
    # [Environment]::SetEnvironmentVariable would flatten them.
    $EnvKey = 'HKCU:\Environment'
    function Get-UserPath {
        $raw = (Get-Item -LiteralPath $EnvKey).GetValue('Path', '', 'DoNotExpandEnvironmentNames')
        return @($raw -split ';' | Where-Object { $_ -ne '' })
    }

    function Set-UserPath([string[]]$Entries) {
        Set-ItemProperty -LiteralPath $EnvKey -Name 'Path' -Value ($Entries -join ';') -Type ExpandString
        # Setting any user variable through .NET broadcasts WM_SETTINGCHANGE,
        # so Explorer and newly opened terminals pick up the new PATH.
        [Environment]::SetEnvironmentVariable('AyrPlayInstaller', '1', 'User')
        [Environment]::SetEnvironmentVariable('AyrPlayInstaller', $null, 'User')
    }

    # Get-HttpStatus returns the HTTP status code carried by a web cmdlet error
    # (Windows PowerShell 5.1 and PowerShell 7 shapes), or 0.
    function Get-HttpStatus($ErrorRecord) {
        $response = $ErrorRecord.Exception.Response
        if ($null -eq $response) { return 0 }
        try { return [int]$response.StatusCode } catch { return 0 }
    }

    # Find-Release asks the GitHub API which assets a release has, so a missing
    # file gets a clear explanation instead of a raw download error. Returns
    # $null when the API is unreachable or rate-limited; callers then fall back
    # to the release's direct download links.
    function Find-Release {
        $api = if ($Version -eq 'latest') {
            "https://api.github.com/repos/$Repo/releases/latest"
        } else {
            "https://api.github.com/repos/$Repo/releases/tags/$Version"
        }
        try {
            $release = Invoke-RestMethod -Uri $api -UserAgent $UserAgent -Headers @{ Accept = 'application/vnd.github+json' }
        } catch {
            if ((Get-HttpStatus $_) -eq 404) {
                if ($Version -eq 'latest') {
                    throw "No AyrPlay release has been published yet ($ReleasesPage). Build it from source instead: $BuildFromSource"
                }
                throw "Release '$Version' was not found. See $ReleasesPage for available versions."
            }
            Write-Warning "Could not ask GitHub which files release '$Version' has ($($_.Exception.Message)); trying its download links directly."
            return $null
        }
        $assets = @{}
        foreach ($item in $release.assets) { $assets[$item.name] = $item.browser_download_url }
        return @{ Tag = $release.tag_name; Assets = $assets }
    }

    # Save-Url downloads to a file, retrying transient failures. Keep-alive is
    # off because Windows PowerShell 5.1 reports a reused, server-closed
    # connection as "The connection was closed unexpectedly". If .NET still
    # fails, curl.exe (built into Windows 10 1803+) gets a try.
    function Save-Url([string]$Url, [string]$OutFile) {
        $lastError = $null
        for ($attempt = 1; $attempt -le 3; $attempt++) {
            try {
                Invoke-WebRequest -Uri $Url -OutFile $OutFile -UseBasicParsing -DisableKeepAlive -UserAgent $UserAgent
                return
            } catch {
                $lastError = $_
                if ((Get-HttpStatus $_) -eq 404) {
                    throw "Not found: $Url. That release may not include this file; see $ReleasesPage, or build AyrPlay yourself: $BuildFromSource"
                }
                Start-Sleep -Seconds (2 * $attempt)
            }
        }
        $curl = Get-Command curl.exe -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
        if ($curl) {
            & $curl.Source --fail --location --silent --show-error --retry 3 --user-agent $UserAgent --output $OutFile $Url
            if ($LASTEXITCODE -eq 0) { return }
        }
        throw "Could not download $Url ($($lastError.Exception.Message)). Check your internet connection or proxy, or download it in a browser from $ReleasesPage and run this script with -AssetDir <folder>."
    }

    if ($Uninstall) {
        Stop-AyrPlay
        Remove-Item -LiteralPath (Get-ShortcutPath) -Force -ErrorAction SilentlyContinue
        $entries = Get-UserPath
        if ($entries -contains $InstallDir) {
            Set-UserPath ($entries | Where-Object { $_ -ne $InstallDir })
        }
        Remove-Item -LiteralPath $InstallDir -Recurse -Force -ErrorAction SilentlyContinue
        Write-Host 'AyrPlay removed. Settings and logs in %AppData%\doubletake were left in place.'
        return
    }

    # 1. Get AyrPlay.exe and SHA256SUMS.txt, then verify the exe against them.
    $asset = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'AyrPlay-arm64.exe' } else { 'AyrPlay.exe' }
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $download = Join-Path $InstallDir 'AyrPlay.exe.download'
    $sums = Join-Path $InstallDir 'SHA256SUMS.txt'

    if ($AssetDir) {
        Write-Host "Installing $asset from $AssetDir..."
        foreach ($name in @($asset, 'SHA256SUMS.txt')) {
            if (-not (Test-Path -LiteralPath (Join-Path $AssetDir $name))) {
                throw "$AssetDir does not contain $name."
            }
        }
        Copy-Item -LiteralPath (Join-Path $AssetDir $asset) -Destination $download -Force
        Copy-Item -LiteralPath (Join-Path $AssetDir 'SHA256SUMS.txt') -Destination $sums -Force
    } else {
        $release = Find-Release
        if ($release) {
            $label = $release.Tag
            if (-not $release.Assets.ContainsKey($asset)) {
                throw "AyrPlay release $label does not include $asset (it was published before AyrPlay shipped as a single exe). A newer release is needed: $ReleasesPage. Or build it yourself: $BuildFromSource"
            }
            if (-not $release.Assets.ContainsKey('SHA256SUMS.txt')) {
                throw "AyrPlay release $label has no SHA256SUMS.txt, so $asset cannot be verified. Nothing was installed."
            }
            $assetUrl = $release.Assets[$asset]
            $sumsUrl = $release.Assets['SHA256SUMS.txt']
        } else {
            $label = $Version
            $base = if ($Version -eq 'latest') { "$ReleasesPage/latest/download" } else { "$ReleasesPage/download/$Version" }
            $assetUrl = "$base/$asset"
            $sumsUrl = "$base/SHA256SUMS.txt"
        }
        Write-Host "Downloading $asset ($label)..."
        Save-Url $assetUrl $download
        Save-Url $sumsUrl $sums
    }

    $pattern = '^([0-9a-fA-F]{64})\s+\*?' + [regex]::Escape($asset) + '$'
    $expected = Get-Content -LiteralPath $sums | ForEach-Object {
        if ($_ -match $pattern) { $Matches[1] }
    } | Select-Object -First 1
    $actual = (Get-FileHash -LiteralPath $download -Algorithm SHA256).Hash
    Remove-Item -LiteralPath $sums -Force
    if (-not $expected -or $actual -ne $expected) {
        Remove-Item -LiteralPath $download -Force
        throw "Checksum mismatch for $asset (expected '$expected', got '$actual'). Nothing was installed."
    }
    Write-Host "Verified SHA-256 $actual"

    Stop-AyrPlay
    Move-Item -LiteralPath $download -Destination $Exe -Force

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
    # The install succeeded even if `doctor` (a native command) reported missing
    # GStreamer plugins; don't leak its exit code to callers.
    $global:LASTEXITCODE = 0
}
