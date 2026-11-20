$ErrorActionPreference = 'Stop'
$scratch = Join-Path $env:LOCALAPPDATA 'DeskPulseVmEvidence'
New-Item -ItemType Directory -Path $scratch -Force | Out-Null
$share = '\\host.lan\Data'

function Write-Evidence($Name, $Value) {
    $pending = Join-Path $share "$Name.next"
    [System.IO.File]::WriteAllText($pending, ($Value | ConvertTo-Json -Compress))
    Move-Item -LiteralPath $pending -Destination (Join-Path $share $Name)
}

function Run-Installer([string]$Arguments) {
    $start = [System.Diagnostics.ProcessStartInfo]::new("$env:SystemRoot\System32\msiexec.exe")
    $start.UseShellExecute = $false
    $start.Arguments = "$Arguments /qn /norestart"
    $process = [System.Diagnostics.Process]::Start($start)
    try {
        if (-not $process.WaitForExit(90000) -or $process.ExitCode -ne 0) {
            throw 'Windows Installer did not complete successfully.'
        }
    } finally {
        $process.Dispose()
    }
}

function Wait-For([scriptblock]$Condition, [int]$Seconds) {
    $deadline = [DateTime]::UtcNow.AddSeconds($Seconds)
    do {
        if (& $Condition) { return }
        Start-Sleep -Milliseconds 500
    } while ([DateTime]::UtcNow -lt $deadline)
    throw 'Timed out waiting for the expected VM state.'
}

try {
    Wait-For { Test-Path -LiteralPath (Join-Path $share 'host-ready.txt') -PathType Leaf } 90
    $os = Get-CimInstance Win32_OperatingSystem
    $cpu = Get-CimInstance Win32_Processor | Select-Object -First 1
    if ($os.ProductType -ne 1 -or [int]$os.BuildNumber -lt 22000 -or $cpu.Architecture -ne 9) {
        throw 'Expected a Windows 11 client with an x64 processor.'
    }

    $package = 'C:\OEM\deskpulse-agent-x64.msi'
    $installed = Join-Path $env:LOCALAPPDATA 'DeskPulse Agent Program\deskpulse-agent.exe'
    $expectedHash = (Get-FileHash -LiteralPath 'C:\OEM\deskpulse-agent.exe').Hash
    $renderer = [scriptblock]::Create((Get-Content -LiteralPath 'C:\OEM\test-windows-render.ps1' -Raw))
    Run-Installer ('/i "{0}"' -f $package)
    try {
        if ((Get-FileHash -LiteralPath $installed).Hash -ne $expectedHash) { throw 'Installed binary does not match the CI build.' }
        & $renderer -Executable $installed -ScratchDirectory $scratch
        Write-Evidence 'console.json' @{build=$os.BuildNumber; architecture='x64'; sha256=$expectedHash; install=$true; fourDPI=$true}

        Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class DeskPulseRdpCheck {
    [DllImport("wtsapi32.dll", CharSet=CharSet.Unicode)]
    private static extern bool WTSQuerySessionInformation(IntPtr server, int session, int field, out IntPtr value, out int size);
    [DllImport("wtsapi32.dll")]
    private static extern void WTSFreeMemory(IntPtr value);
    public static int Query(int field) {
        IntPtr value;
        int size;
        if (!WTSQuerySessionInformation(IntPtr.Zero, -1, field, out value, out size)) return -1;
        try { return size == 2 ? Marshal.ReadInt16(value) : size == 4 ? Marshal.ReadInt32(value) : -1; }
        finally { WTSFreeMemory(value); }
    }
}
'@
        Wait-For { [DeskPulseRdpCheck]::Query(16) -eq 2 -and [DeskPulseRdpCheck]::Query(8) -eq 0 } 180
        & $renderer -Executable $installed -ScratchDirectory $scratch
        Write-Evidence 'rdp-first.json' @{protocol='RDP'; active=$true; rendered=$true}
        Wait-For { [DeskPulseRdpCheck]::Query(8) -in 4,5 } 80
        Write-Evidence 'rdp-disconnected.json' @{disconnected=$true}
        Wait-For { [DeskPulseRdpCheck]::Query(16) -eq 2 -and [DeskPulseRdpCheck]::Query(8) -eq 0 } 120
        & $renderer -Executable $installed -ScratchDirectory $scratch
        Write-Evidence 'rdp-reconnected.json' @{reconnected=$true; rendered=$true}
    } finally {
        Run-Installer ('/x "{0}"' -f $package)
    }
    if (Test-Path -LiteralPath $installed) { throw 'Desktop executable remained after uninstall.' }
    Write-Evidence 'passed.json' @{build=$os.BuildNumber; architecture='x64'; install=$true; uninstall=$true; rdpReconnect=$true}
} catch {
    Write-Evidence 'failed.json' @{error=$_.Exception.Message; line=$_.InvocationInfo.ScriptLineNumber}
    exit 1
}
