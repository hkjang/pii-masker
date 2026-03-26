$ErrorActionPreference = "Stop"

$baseUrl = if ($env:PII_MASKER_BASE_URL) { $env:PII_MASKER_BASE_URL.TrimEnd("/") } else { "http://localhost:8080" }
$workDir = Join-Path $PSScriptRoot "..\\.smoke"
New-Item -ItemType Directory -Force $workDir | Out-Null

$pngPath = Join-Path $workDir "sample.png"
$pdfPath = Join-Path $workDir "sample.pdf"

Add-Type -AssemblyName System.Drawing
$bitmap = New-Object System.Drawing.Bitmap 400, 200
try {
    $graphics = [System.Drawing.Graphics]::FromImage($bitmap)
    try {
        $graphics.Clear([System.Drawing.Color]::White)
    } finally {
        $graphics.Dispose()
    }
    $bitmap.Save($pngPath, [System.Drawing.Imaging.ImageFormat]::Png)
} finally {
    $bitmap.Dispose()
}

function New-BlankPdfBytes {
    param(
        [int]$Width = 400,
        [int]$Height = 400
    )

    $objects = @(
        "1 0 obj`n<< /Type /Catalog /Pages 2 0 R >>`nendobj`n",
        "2 0 obj`n<< /Type /Pages /Kids [3 0 R] /Count 1 >>`nendobj`n",
        "3 0 obj`n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 $Width $Height] /Contents 4 0 R >>`nendobj`n",
        "4 0 obj`n<< /Length 0 >>`nstream`n`nendstream`nendobj`n"
    )

    $builder = New-Object System.Text.StringBuilder
    [void]$builder.Append("%PDF-1.4`n")
    $offsets = New-Object System.Collections.Generic.List[int]
    [void]$offsets.Add(0)

    foreach ($obj in $objects) {
        [void]$offsets.Add($builder.Length)
        [void]$builder.Append($obj)
    }

    $xrefOffset = $builder.Length
    [void]$builder.Append("xref`n0 5`n")
    [void]$builder.Append("0000000000 65535 f `n")
    foreach ($offset in $offsets | Select-Object -Skip 1) {
        [void]$builder.Append(($offset.ToString("D10")) + " 00000 n `n")
    }
    [void]$builder.Append("trailer`n<< /Size 5 /Root 1 0 R >>`n")
    [void]$builder.Append("startxref`n")
    [void]$builder.Append("$xrefOffset`n")
    [void]$builder.Append("%%EOF`n")

    return [Text.Encoding]::ASCII.GetBytes($builder.ToString())
}

[IO.File]::WriteAllBytes($pdfPath, (New-BlankPdfBytes))

Write-Host "Testing /v1/test-connection"
$conn = Invoke-RestMethod -Method Post -Uri "$baseUrl/v1/test-connection"
if (-not $conn.ok) { throw "connection test failed" }

Write-Host "Testing /v1/mask with PNG"
$pngResponse = Invoke-WebRequest -Method Post -Uri "$baseUrl/v1/mask" -Form @{ file = Get-Item $pngPath }
if (-not $pngResponse.Headers["Content-Type"].StartsWith("multipart/mixed")) {
    throw "unexpected /v1/mask content-type: $($pngResponse.Headers["Content-Type"])"
}

Write-Host "Testing /v1/jobs with PDF"
$job = Invoke-RestMethod -Method Post -Uri "$baseUrl/v1/jobs" -Form @{ file = Get-Item $pdfPath }
$jobId = $job.job_id
if (-not $jobId) { throw "job_id missing" }

for ($i = 0; $i -lt 20; $i++) {
    Start-Sleep -Milliseconds 250
    $status = Invoke-RestMethod -Method Get -Uri "$baseUrl/v1/jobs/$jobId"
    if ($status.status -eq "completed") { break }
}

$finalStatus = Invoke-RestMethod -Method Get -Uri "$baseUrl/v1/jobs/$jobId"
if ($finalStatus.status -ne "completed") {
    throw "job did not complete: $($finalStatus | ConvertTo-Json -Depth 10)"
}

$null = Invoke-WebRequest -Method Get -Uri "$baseUrl/v1/jobs/$jobId/result" -OutFile (Join-Path $workDir "masked_sample.pdf")
Write-Host "Smoke test completed successfully"
