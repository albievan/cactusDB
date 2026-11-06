# Downloads the cactusdb binary for this computer (Windows on Intel/AMD or
# ARM) from GitHub and saves it as "cactusdb.exe".
#
#   irm https://raw.githubusercontent.com/albievan/cactusDB/main/setup.ps1 | iex
#
# or, after downloading this file:
#
#   powershell -ExecutionPolicy Bypass -File setup.ps1 C:\cactusdb
#
# The folder to install into is the first argument, or $env:CACTUSDB_DIR, or
# the current folder. cactusdb keeps its saved connections and exports next to
# itself, so give it a folder of its own. $env:CACTUSDB_REF picks the branch or
# tag to download from (default main). Running it again updates cactusdb.
# In PowerShell 7 on Linux or macOS it installs that system's build as "cactusdb".
param([string]$Dir = $(if ($env:CACTUSDB_DIR) { $env:CACTUSDB_DIR } else { "." }))

$ErrorActionPreference = "Stop"
$ProgressPreference = "SilentlyContinue" # the progress bar makes Invoke-WebRequest very slow in Windows PowerShell
$repo = "albievan/cactusDB"
$ref = if ($env:CACTUSDB_REF) { $env:CACTUSDB_REF } else { "main" }

if ($IsLinux -or $IsMacOS) {
    $os = if ($IsLinux) { "linux" } else { "darwin" }
    $machine = (uname -m)
} else {
    $os = "windows"
    # A 32-bit PowerShell on 64-bit Windows reports x86 here and the real one in PROCESSOR_ARCHITEW6432.
    $machine = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
}
switch -Regex ($machine) {
    "^(AMD64|x86_64)$" { $arch = "amd64" }
    "^(ARM64|aarch64|arm64)$" { $arch = "arm64" }
    default { throw "no cactusdb build for the $machine processor (there are amd64 and arm64 builds)" }
}

$file = "cactusdb-$os-$arch"
$name = "cactusdb"
if ($os -eq "windows") { $file += ".exe"; $name += ".exe" }
$url = "https://github.com/$repo/raw/$ref/dist/$file"
$Dir = (New-Item -ItemType Directory -Force -Path $Dir).FullName
$dest = Join-Path $Dir $name
$tmp = Join-Path $Dir ".cactusdb.download.$PID$(if ($os -eq "windows") { ".exe" })" # Windows runs only .exe files

Write-Host "downloading $file ($ref) to $dest"
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
try {
    Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $tmp
    if ($os -ne "windows") { chmod +x $tmp }
    # Check it runs before replacing an existing cactusdb.
    $version = & $tmp --version
    if ($LASTEXITCODE -ne 0) { throw "the downloaded file does not run: $version" }
    if (Test-Path $dest) {
        # Windows can't overwrite a cactusdb that is running, but it can rename it.
        # A copy still running stays behind until the next update removes it.
        Get-ChildItem -Force -Path $Dir -Filter "$name.old.*" | Remove-Item -Force -ErrorAction SilentlyContinue
        $old = "$dest.old.$PID"
        Move-Item $dest $old
        Remove-Item -Force $old -ErrorAction SilentlyContinue
    }
    Move-Item -Force $tmp $dest
} catch {
    Remove-Item -Force $tmp -ErrorAction SilentlyContinue
    throw "setup: $($_.Exception.Message) ($url)"
}

Write-Host "installed $version as $dest"
if ($os -eq "windows") {
    Write-Host "start it with:  cd `"$Dir`"; .\cactusdb.exe --port 8080"
} else {
    Write-Host "start it with:  cd `"$Dir`" && ./cactusdb --port 8080"
}
