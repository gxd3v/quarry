# Installs Quarry for the current user on Windows (x64).
#
#   irm https://raw.githubusercontent.com/gxd3v/quarry/main/install.ps1 | iex
#
# $env:QUARRY_VERSION picks a release tag (default: the latest release),
# $env:QUARRY_INSTALL_DIR the folder (default: %LOCALAPPDATA%\Programs\Quarry)
# and $env:QUARRY_NO_SHORTCUT=1 skips the Start menu shortcut. The folder is
# user-writable, so Quarry updates itself in place.

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$repo = 'gxd3v/quarry'
$version = if ($env:QUARRY_VERSION) { $env:QUARRY_VERSION } else { 'latest' }
$base = if ($version -eq 'latest') { "https://github.com/$repo/releases/latest/download" } else { "https://github.com/$repo/releases/download/$version" }
$dir = if ($env:QUARRY_INSTALL_DIR) { $env:QUARRY_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\Quarry' }
$asset = 'quarry-windows-amd64.zip'

if (-not [Environment]::Is64BitOperatingSystem) { throw 'Quarry needs 64-bit Windows.' }

$tmp = Join-Path ([IO.Path]::GetTempPath()) ("quarry-install-" + [Guid]::NewGuid())
New-Item -ItemType Directory -Force $tmp | Out-Null
try {
    Write-Host "Downloading $asset ($version)..."
    Invoke-WebRequest -UseBasicParsing -Uri "$base/$asset" -OutFile (Join-Path $tmp $asset)
    Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS" -OutFile (Join-Path $tmp 'SHA256SUMS')

    $want = Get-Content (Join-Path $tmp 'SHA256SUMS') | ForEach-Object {
        $f = $_ -split '\s+'
        if ($f.Count -ge 2 -and $f[1].TrimStart('*') -eq $asset) { $f[0].ToLower() }
    } | Select-Object -First 1
    if (-not $want) { throw "SHA256SUMS lists no $asset." }
    $got = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $asset)).Hash.ToLower()
    if ($got -ne $want) { throw "Checksum mismatch for $asset." }

    Expand-Archive -Path (Join-Path $tmp $asset) -DestinationPath (Join-Path $tmp 'app') -Force
    New-Item -ItemType Directory -Force $dir | Out-Null
    $exe = Join-Path $dir 'quarry.exe'
    # A running copy can be renamed but not overwritten.
    if (Test-Path $exe) {
        Remove-Item -Force "$exe.old" -ErrorAction SilentlyContinue
        Rename-Item $exe 'quarry.exe.old'
    }
    Move-Item (Join-Path $tmp 'app\quarry.exe') $exe

    if ($env:QUARRY_NO_SHORTCUT -ne '1') {
        $menu = Join-Path ([Environment]::GetFolderPath('Programs')) 'Quarry.lnk'
        $shell = New-Object -ComObject WScript.Shell
        $link = $shell.CreateShortcut($menu)
        $link.TargetPath = $exe
        $link.WorkingDirectory = $dir
        $link.Description = 'Database IDE for PostgreSQL, Oracle, MongoDB and Redis'
        $link.Save()
    }
    Write-Host "Installed Quarry in $dir"
    Write-Host 'It starts from the Start menu (Quarry).'
}
finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}
