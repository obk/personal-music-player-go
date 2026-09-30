<#
.SYNOPSIS
Builds the music player with Go and the MSYS2 UCRT64 toolchain (gcc for cgo, FFmpeg, pkg-config).

.DESCRIPTION
  scripts\build.ps1            build\MusicPlayer\musicplayer.exe plus every DLL it needs (runs from anywhere)
  scripts\build.ps1 -Package   the same, zipped to build\MusicPlayer-windows-x64.zip
  scripts\build.ps1 -Installer the same, plus build\MusicPlayer-<version>-setup-x64.exe (needs Inno Setup 6)
  scripts\build.ps1 -Console   keep a console window (log output and panics are visible)
  scripts\build.ps1 -Test      run the test suite

The UCRT64 directory is found from $env:MSYS2_UCRT64, a Scoop install of msys2, or C:\msys64\ucrt64.
ISCC.exe is found on PATH, in a Scoop install of inno-setup, or in Inno Setup's default install folders.
#>
param([switch]$Package, [switch]$Installer, [switch]$Console, [switch]$Test)
$ErrorActionPreference = 'Stop'

$root = Split-Path -Parent $PSScriptRoot
Set-Location $root

$ucrt = @($env:MSYS2_UCRT64, "$env:USERPROFILE\scoop\apps\msys2\current\ucrt64", 'C:\msys64\ucrt64') |
    Where-Object { $_ -and (Test-Path (Join-Path $_ 'bin\gcc.exe')) } | Select-Object -First 1
if (-not $ucrt) {
    throw 'MSYS2 UCRT64 not found. Install MSYS2 and run: pacman -S --needed mingw-w64-ucrt-x86_64-{toolchain,pkgconf,ffmpeg}'
}
$env:PATH = "$ucrt\bin;$env:PATH"
$env:CGO_ENABLED = '1'
$env:CC = 'gcc'

if ($Test) {
    go test ./...
    exit $LASTEXITCODE
}

# Icon and version resource, linked in by `go build` as a .syso object.
$cmd = 'cmd\musicplayer'
go run ./cmd/mkicon "$cmd\icon.ico"
if ($LASTEXITCODE) { exit $LASTEXITCODE }
windres -i "$cmd\musicplayer.rc" -O coff -o "$cmd\rsrc_windows_amd64.syso"
if ($LASTEXITCODE) { exit $LASTEXITCODE }

$out = 'build\MusicPlayer'
New-Item -ItemType Directory -Force $out | Out-Null
$ldflags = '-s -w'
if (-not $Console) { $ldflags += ' -H windowsgui' }
go build -trimpath -ldflags $ldflags -o "$out\musicplayer.exe" ./cmd/musicplayer
if ($LASTEXITCODE) { exit $LASTEXITCODE }

# Copy the non-system DLLs (FFmpeg and its dependencies, the MinGW runtime) next to the executable, following
# imports until nothing new appears.
$seen = @{}
$queue = [System.Collections.Generic.Queue[string]]::new()
$queue.Enqueue((Resolve-Path "$out\musicplayer.exe").Path)
while ($queue.Count) {
    $file = $queue.Dequeue()
    foreach ($line in (objdump -p $file | Select-String 'DLL Name: (.+)$')) {
        $dll = $line.Matches[0].Groups[1].Value.Trim()
        if ($seen.ContainsKey($dll.ToLower())) { continue }
        $seen[$dll.ToLower()] = $true
        $src = Join-Path "$ucrt\bin" $dll
        if (Test-Path $src) {
            Copy-Item $src $out -Force
            $queue.Enqueue($src)
        }
    }
}
$files = Get-ChildItem $out -File
$size = ($files | Measure-Object Length -Sum).Sum / 1MB
Write-Host ("Built {0}\musicplayer.exe with {1} DLLs ({2:N0} MB)" -f $out, ($files.Count - 1), $size)

if ($Package) {
    $zip = 'build\MusicPlayer-windows-x64.zip'
    Compress-Archive -Path $out -DestinationPath $zip -Force
    Write-Host ("Archive: {0} ({1:N0} MB)" -f $zip, ((Get-Item $zip).Length / 1MB))
}

if ($Installer) {
    $iscc = @((Get-Command ISCC.exe -ErrorAction SilentlyContinue).Source,
        "$env:USERPROFILE\scoop\apps\inno-setup\current\ISCC.exe",
        "$env:LOCALAPPDATA\Programs\Inno Setup 6\ISCC.exe",
        "${env:ProgramFiles(x86)}\Inno Setup 6\ISCC.exe",
        "$env:ProgramFiles\Inno Setup 6\ISCC.exe") |
        Where-Object { $_ -and (Test-Path $_) } | Select-Object -First 1
    if (-not $iscc) {
        throw 'Inno Setup 6 not found. Install it with: scoop install extras/inno-setup (or winget install JRSoftware.InnoSetup)'
    }
    # From the version resource in cmd\musicplayer\musicplayer.rc: 2.0.0.0 -> 2.0.0.
    $v = [version](Get-Item "$out\musicplayer.exe").VersionInfo.ProductVersion
    $version = '{0}.{1}.{2}' -f $v.Major, $v.Minor, $v.Build
    & $iscc /Q "/DAppVersion=$version" installer\musicplayer.iss
    if ($LASTEXITCODE) { exit $LASTEXITCODE }
    $setup = "build\MusicPlayer-$version-setup-x64.exe"
    Write-Host ("Installer: {0} ({1:N0} MB)" -f $setup, ((Get-Item $setup).Length / 1MB))
}
