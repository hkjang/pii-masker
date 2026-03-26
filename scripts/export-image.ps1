[CmdletBinding()]
param(
    [string]$ImageRef = "pii-masker:latest",
    [string]$OutputPath = "pii-masker-image.tar.gz"
)

$ErrorActionPreference = "Stop"

$resolvedOutput = [System.IO.Path]::GetFullPath((Join-Path (Get-Location) $OutputPath))
if ($resolvedOutput.EndsWith(".gz", [System.StringComparison]::OrdinalIgnoreCase)) {
    $tarPath = $resolvedOutput.Substring(0, $resolvedOutput.Length - 3)
} else {
    $tarPath = "$resolvedOutput.tar"
}

Write-Host "Saving Docker image '$ImageRef' to $tarPath"
docker image save $ImageRef -o $tarPath
if ($LASTEXITCODE -ne 0) {
    throw "docker image save failed for $ImageRef"
}

try {
    $inputStream = [System.IO.File]::OpenRead($tarPath)
    $outputStream = [System.IO.File]::Create($resolvedOutput)
    $gzipStream = New-Object System.IO.Compression.GzipStream($outputStream, [System.IO.Compression.CompressionLevel]::Optimal)
    $inputStream.CopyTo($gzipStream)
}
finally {
    if ($gzipStream) { $gzipStream.Dispose() }
    if ($outputStream) { $outputStream.Dispose() }
    if ($inputStream) { $inputStream.Dispose() }
}

Remove-Item $tarPath -Force
Write-Host "Exported image archive: $resolvedOutput"
