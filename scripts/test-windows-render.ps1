param(
    [Parameter(Mandatory)][string]$Executable,
    [Parameter(Mandatory)][string]$ScratchDirectory
)

$ErrorActionPreference = 'Stop'
$scratch = Join-Path $ScratchDirectory ('deskpulse-render-' + [guid]::NewGuid().ToString('N'))
$capture = Join-Path $scratch 'capture'
$data = Join-Path $scratch 'data'
$start = [System.Diagnostics.ProcessStartInfo]::new((Resolve-Path -LiteralPath $Executable).Path)
$start.UseShellExecute = $false
foreach ($argument in @('--synthetic', '--data-dir', $data, '--capture-dir', $capture)) {
    $start.ArgumentList.Add($argument)
}

$process = [System.Diagnostics.Process]::Start($start)
try {
    if (-not $process.WaitForExit(30000)) {
        $process.Kill()
        throw 'DeskPulse renderer timed out.'
    }
    if ($process.ExitCode -ne 0) {
        $log = Join-Path $capture 'capture-error.txt'
        if (Test-Path -LiteralPath $log -PathType Leaf) { Get-Content -LiteralPath $log }
        throw "DeskPulse renderer exited with code $($process.ExitCode)."
    }
} finally {
    $process.Dispose()
}

foreach ($scale in @('100%', '125%', '150%', '200%')) {
    foreach ($frame in @('ball.png', 'hover.png', 'settings-deepseek.png', 'analysis-details.png')) {
        $path = Join-Path $capture "$scale/$frame"
        if (-not (Test-Path -LiteralPath $path -PathType Leaf)) { throw "Missing capture $path" }
        if ((Get-Item -LiteralPath $path).Length -le 8) { throw "Empty capture $path" }
    }
}

Write-Output 'DeskPulse rendered the floating UI, settings and expanded diagnosis at four scales.'
