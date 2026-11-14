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
# itself, so give it a folder of its own. $env:CACTUSDB_VERSION picks the
# version, e.g. 1.61.0 (default: the latest release). Running it again
# updates cactusdb.
# In PowerShell 7 on Linux or macOS it installs that system's build as "cactusdb".
param([string]$Dir = $(if ($env:CACTUSDB_DIR) { $env:CACTUSDB_DIR } else { "." }))

$ErrorActionPreference = "Stop"

# Ed25519 signature check (RFC 8032) in plain PowerShell, for Windows, which has
# none built in: $true when $Sig (64 bytes) is $Msg signed with $Pub (32 bytes).
function Test-Ed25519([byte[]]$Pub, [byte[]]$Msg, [byte[]]$Sig) {
    Add-Type -AssemblyName System.Numerics
    $BI = [System.Numerics.BigInteger]
    $p = $BI::Pow(2, 255) - 19
    $L = $BI::Pow(2, 252) + $BI::Parse("27742317777372353535851937790883648493")
    $md = { param($a) $r = $BI::Remainder($a, $p); if ($r.Sign -lt 0) { $r + $p } else { $r } }
    $inv = { param($a) $BI::ModPow($a, $p - 2, $p) }
    $num = { param([byte[]]$b) $BI::new([byte[]]($b + [byte[]]@(0))) } # little-endian, unsigned
    $d = & $md ($BI::Multiply(-121665, (& $inv 121666)))
    $d2 = & $md (2 * $d)
    $I = $BI::ModPow(2, $BI::Divide($p - 1, 4), $p) # sqrt(-1)
    $add = { param($P1, $P2)
        $A = & $md (($P1[1] - $P1[0]) * ($P2[1] - $P2[0])); $B = & $md (($P1[1] + $P1[0]) * ($P2[1] + $P2[0]))
        $C = & $md ($P1[3] * $d2 * $P2[3]); $D = & $md (2 * $P1[2] * $P2[2])
        $E = $B - $A; $F = $D - $C; $G = $D + $C; $H = $B + $A
        , @((& $md ($E * $F)), (& $md ($G * $H)), (& $md ($F * $G)), (& $md ($E * $H))) }
    $mul = { param($s, $P1)
        $Q = @([System.Numerics.BigInteger]::Zero, [System.Numerics.BigInteger]::One, [System.Numerics.BigInteger]::One, [System.Numerics.BigInteger]::Zero)
        $bits = $s.ToByteArray()
        for ($i = 255; $i -ge 0; $i--) {
            $Q = & $add $Q $Q
            $k = [int][math]::Floor($i / 8)
            if ($k -lt $bits.Length -and (($bits[$k] -shr ($i % 8)) -band 1)) { $Q = & $add $Q $P1 } }
        , $Q }
    $decode = { param([byte[]]$b)
        $c = [byte[]]$b.Clone(); $sign = ($c[31] -shr 7) -band 1; $c[31] = $c[31] -band 0x7f
        $y = & $num $c
        if ($y -ge $p) { return $null }
        $u = & $md ($y * $y - 1); $v = & $md ($d * $y * $y + 1)
        $x = $BI::ModPow((& $md ($u * (& $inv $v))), $BI::Divide($p + 3, 8), $p)
        if ((& $md ($v * $x * $x - $u)) -ne 0) { $x = & $md ($x * $I) }
        if ((& $md ($v * $x * $x - $u)) -ne 0) { return $null }
        if ($x.IsZero -and $sign -eq 1) { return $null }
        if ([int]$BI::Remainder($x, 2) -ne $sign) { $x = $p - $x }
        , @($x, $y, [System.Numerics.BigInteger]::One, (& $md ($x * $y))) }
    if ($Pub.Length -ne 32 -or $Sig.Length -ne 64) { return $false }
    $A = & $decode $Pub; $R = & $decode ([byte[]]$Sig[0..31])
    if ($null -eq $A -or $null -eq $R) { return $false }
    $S = & $num ([byte[]]$Sig[32..63])
    if ($S -ge $L) { return $false }
    $sha = [System.Security.Cryptography.SHA512]::Create()
    $h = $BI::Remainder((& $num $sha.ComputeHash([byte[]]($Sig[0..31] + $Pub + $Msg))), $L)
    $gy = & $md (4 * (& $inv 5))
    $Bpt = & $decode ([byte[]]($gy.ToByteArray() + [byte[]]::new(32))[0..31])
    $left = & $mul $S $Bpt
    $right = & $add $R (& $mul $h $A)
    return ((& $md ($left[0] * $right[2] - $right[0] * $left[2])) -eq 0) -and ((& $md ($left[1] * $right[2] - $right[1] * $left[2])) -eq 0)
}

$ProgressPreference = "SilentlyContinue" # the progress bar makes Invoke-WebRequest very slow in Windows PowerShell
$repo = "albievan/cactusDB"
$ver = if ($env:CACTUSDB_VERSION) { $env:CACTUSDB_VERSION.TrimStart("v") } else { "" }
if ($ver -and $ver -notmatch '^[0-9.]+$') { throw "setup: CACTUSDB_VERSION=$ver isn't a version like 1.61.0" }
if ($ver) { $base = "https://github.com/$repo/releases/download/v$ver"; $ref = $ver }
else { $base = "https://github.com/$repo/releases/latest/download"; $ref = "latest" }

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
$url = "$base/$file"
$Dir = (New-Item -ItemType Directory -Force -Path $Dir).FullName
$dest = Join-Path $Dir $name
$tmp = Join-Path $Dir ".cactusdb.download.$PID$(if ($os -eq "windows") { ".exe" })" # Windows runs only .exe files

Write-Host "downloading $file ($ref) to $dest"
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
try {
    Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $tmp
    # Check it against the published checksums before running it.
    $sumsFile = "$tmp.sums"
    Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS" -OutFile $sumsFile
    $sums = [IO.File]::ReadAllText($sumsFile)
    # The checksums are signed with the cactusdb release key (SHA256SUMS.sig):
    # checked here, so a changed file on GitHub isn't installed.
    Invoke-WebRequest -UseBasicParsing -Uri "$base/SHA256SUMS.sig" -OutFile "$tmp.sig"
    $pub = [Convert]::FromBase64String("GDkn+i/onn7P5N3QN3Rb3EyJFH5FU5kLF40AR36lcn8=")
    $sig = [Convert]::FromBase64String(([IO.File]::ReadAllText("$tmp.sig")).Trim())
    if (-not (Test-Ed25519 $pub ([IO.File]::ReadAllBytes($sumsFile)) $sig)) { throw "SHA256SUMS isn't signed with the cactusdb release key: not installed" }
    Write-Host "release signature checked"
    $want = ($sums -split "`n" | ForEach-Object { $f = -split $_; if ($f.Count -eq 2 -and $f[1].TrimStart('*') -eq $file) { $f[0] } } | Select-Object -First 1)
    if (-not $want) { throw "SHA256SUMS has no checksum for $file" }
    if ((Get-FileHash -Algorithm SHA256 -Path $tmp).Hash -ne $want.ToUpper()) { throw "the download of $file is damaged: its SHA-256 isn't the one in SHA256SUMS" }
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
    Remove-Item -Force "$tmp.sums", "$tmp.sig" -ErrorAction SilentlyContinue
} catch {
    Remove-Item -Force $tmp -ErrorAction SilentlyContinue
    Remove-Item -Force "$tmp.sums", "$tmp.sig" -ErrorAction SilentlyContinue
    throw "setup: $($_.Exception.Message) ($url)"
}

Write-Host "installed $version as $dest"
if ($os -eq "windows") {
    Write-Host "start it with:  cd `"$Dir`"; .\cactusdb.exe --port 8080"
} else {
    Write-Host "start it with:  cd `"$Dir`" && ./cactusdb --port 8080"
}
