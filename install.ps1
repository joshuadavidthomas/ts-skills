$ErrorActionPreference = "Stop"

$Repo = "joshuadavidthomas/ts-skills"
$InstallDir = if ($env:TS_SKILLS_INSTALL_DIR) { $env:TS_SKILLS_INSTALL_DIR } else { Join-Path $HOME ".local\bin" }
$Version = if ($env:TS_SKILLS_VERSION) { $env:TS_SKILLS_VERSION } else { "latest" }
$RequireAttestation = $env:TS_SKILLS_REQUIRE_ATTESTATION -eq "1"

$archRaw = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
$arch = switch ($archRaw) {
  "x64" { "amd64" }
  "arm64" { "arm64" }
  default { throw "Unsupported architecture: $archRaw" }
}

$asset = "ts-skills_windows_${arch}.zip"
$checksums = "checksums.txt"

if ($Version -eq "latest") {
  $downloadUrl = "https://github.com/$Repo/releases/latest/download/$asset"
  $checksumsUrl = "https://github.com/$Repo/releases/latest/download/$checksums"
} else {
  $tag = if ($Version.StartsWith("v")) { $Version } else { "v$Version" }
  $downloadUrl = "https://github.com/$Repo/releases/download/$tag/$asset"
  $checksumsUrl = "https://github.com/$Repo/releases/download/$tag/$checksums"
}

$tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("ts-skills-install-" + [System.Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $tmpDir | Out-Null

$zipPath = Join-Path $tmpDir $asset
$checksumsPath = Join-Path $tmpDir $checksums

try {
  Write-Host "Downloading $asset..."
  Invoke-WebRequest -Uri $downloadUrl -OutFile $zipPath
  Invoke-WebRequest -Uri $checksumsUrl -OutFile $checksumsPath

  $expected = $null
  foreach ($line in Get-Content $checksumsPath) {
    if ([string]::IsNullOrWhiteSpace($line)) { continue }
    $parts = $line -split "\s+"
    if ($parts.Count -lt 2) { continue }
    $name = $parts[1].TrimStart("*")
    if ($name -eq $asset) {
      $expected = $parts[0].ToLowerInvariant()
      break
    }
  }

  if (-not $expected) {
    throw "Could not find checksum entry for $asset"
  }

  $actual = (Get-FileHash -Algorithm SHA256 -Path $zipPath).Hash.ToLowerInvariant()
  if ($expected -ne $actual) {
    throw "Checksum mismatch for $asset (expected $expected, got $actual)"
  }

  $provenanceVerified = $false
  if (Get-Command gh -ErrorAction SilentlyContinue) {
    & gh auth status *> $null
    if ($LASTEXITCODE -eq 0) {
      & gh api "repos/$Repo/attestations/sha256:$actual" *> $null
      if ($LASTEXITCODE -eq 0) {
        & gh attestation verify $zipPath --repo $Repo
        if ($LASTEXITCODE -ne 0) {
          throw "Build provenance verification failed for $asset"
        }
        $provenanceVerified = $true
        Write-Host "Build provenance verified."
      }
    }
  }

  if (-not $provenanceVerified) {
    if ($RequireAttestation) {
      throw "TS_SKILLS_REQUIRE_ATTESTATION=1 is set, but provenance verification is unavailable"
    }
    Write-Host "note: skipping build-provenance verification"
  }

  Expand-Archive -Path $zipPath -DestinationPath $tmpDir -Force

  $binaries = @("ts-skills.exe", "ts-skillsd.exe")
  foreach ($binary in $binaries) {
    if (-not (Test-Path (Join-Path $tmpDir $binary) -PathType Leaf)) {
      throw "Release archive did not contain $binary"
    }
  }

  New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
  foreach ($binary in $binaries) {
    $destination = Join-Path $InstallDir $binary
    if (Test-Path $destination -PathType Leaf) {
      try {
        $stream = [System.IO.File]::Open($destination, "Open", "ReadWrite", "None")
        $stream.Dispose()
      } catch {
        throw "Cannot replace $binary while it is in use; stop the running process and try again"
      }
    }
  }
  foreach ($binary in $binaries) {
    Copy-Item -Path (Join-Path $tmpDir $binary) -Destination (Join-Path $InstallDir $binary) -Force
  }
  Set-Content -Path (Join-Path $InstallDir ".ts-skills-managed-by") -Value "install-script" -NoNewline

  Write-Host "Installed ts-skills and ts-skillsd to $InstallDir"
  if (($env:PATH -split ";") -contains $InstallDir) {
    Write-Host "Run: ts-skills version"
  } else {
    Write-Host "Add $InstallDir to PATH, then run: ts-skills version"
    Write-Host "For now: $(Join-Path $InstallDir 'ts-skills.exe') version"
  }
}
finally {
  Remove-Item -Path $tmpDir -Recurse -Force -ErrorAction SilentlyContinue
}
