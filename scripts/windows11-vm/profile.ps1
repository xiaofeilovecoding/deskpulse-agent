param(
    [Parameter(Mandatory)][string]$Executable,
    [Parameter(Mandatory)][string]$ScratchDirectory,
    [int]$WarmupSeconds = 60,
    [int]$Intervals = 180
)

$ErrorActionPreference = 'Stop'
$data = Join-Path $ScratchDirectory ('live-' + [guid]::NewGuid().ToString('N'))
$start = [System.Diagnostics.ProcessStartInfo]::new((Resolve-Path -LiteralPath $Executable).Path)
$start.UseShellExecute = $false
$start.Arguments = '--data-dir "{0}"' -f $data

Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class DeskPulseVmDatabase {
    [DllImport("winsqlite3.dll", CallingConvention=CallingConvention.Cdecl)]
    private static extern int sqlite3_open_v2([MarshalAs(UnmanagedType.LPUTF8Str)] string file, out IntPtr db, int flags, IntPtr vfs);
    [DllImport("winsqlite3.dll", CallingConvention=CallingConvention.Cdecl)]
    private static extern int sqlite3_prepare_v2(IntPtr db, string sql, int bytes, out IntPtr statement, IntPtr tail);
    [DllImport("winsqlite3.dll", CallingConvention=CallingConvention.Cdecl)]
    private static extern int sqlite3_step(IntPtr statement);
    [DllImport("winsqlite3.dll", CallingConvention=CallingConvention.Cdecl)]
    private static extern int sqlite3_column_int(IntPtr statement, int column);
    [DllImport("winsqlite3.dll", CallingConvention=CallingConvention.Cdecl)]
    private static extern int sqlite3_finalize(IntPtr statement);
    [DllImport("winsqlite3.dll", CallingConvention=CallingConvention.Cdecl)]
    private static extern int sqlite3_close(IntPtr db);
    public static int CountSampleKinds(string file) {
        IntPtr db, statement = IntPtr.Zero;
        int opened = sqlite3_open_v2(file, out db, 1, IntPtr.Zero);
        try {
            if (opened != 0) return -1;
            const string sql = "SELECT count(distinct kind) FROM metric_samples WHERE kind IN ('cpu','mem','diskio') AND a IS NOT NULL";
            if (sqlite3_prepare_v2(db, sql, -1, out statement, IntPtr.Zero) != 0 || sqlite3_step(statement) != 100) return -1;
            return sqlite3_column_int(statement, 0);
        } finally {
            if (statement != IntPtr.Zero) sqlite3_finalize(statement);
            if (db != IntPtr.Zero) sqlite3_close(db);
        }
    }
}
'@

function Sample-Idle([System.Diagnostics.Process]$App, [System.Diagnostics.Stopwatch]$Clock) {
    $App.Refresh()
    if ($App.HasExited) { throw 'DeskPulse exited during normal sampling.' }
    $counters = Get-CimInstance Win32_PerfRawData_PerfProc_Process -Filter "IDProcess=$($App.Id)"
    if (-not $counters -or $counters.WorkingSetPrivate -eq 0) { throw 'Private working-set counter is unavailable.' }
    [pscustomobject]@{seconds=$Clock.Elapsed.TotalSeconds; cpu=$App.TotalProcessorTime.TotalSeconds; privateMiB=($counters.WorkingSetPrivate / 1MB)}
}

function Percentile95([double[]]$Values) {
    $ordered = @($Values | Sort-Object)
    return $ordered[[int][Math]::Ceiling($ordered.Count * 0.95) - 1]
}

$app = [System.Diagnostics.Process]::Start($start)
try {
    Start-Sleep -Seconds $WarmupSeconds
    $clock = [System.Diagnostics.Stopwatch]::StartNew()
    $first = Sample-Idle $app $clock
    $last = $first
    $cpu = [System.Collections.Generic.List[double]]::new()
    $memory = [System.Collections.Generic.List[double]]::new()
    for ($interval = 1; $interval -le $Intervals; $interval++) {
        $wait = [int][Math]::Max(0, ($interval * 5 - $clock.Elapsed.TotalSeconds) * 1000)
        if ($wait -gt 0) { Start-Sleep -Milliseconds $wait }
        $now = Sample-Idle $app $clock
        $cpu.Add(100 * ($now.cpu - $last.cpu) / ($now.seconds - $last.seconds))
        $memory.Add($now.privateMiB)
        $last = $now
    }
    $db = Join-Path $data 'deskpulse-agent.db'
    $kinds = [DeskPulseVmDatabase]::CountSampleKinds($db)
    if ($kinds -ne 3) { throw 'The database does not contain CPU, memory and disk I/O samples.' }
    $duration = $last.seconds - $first.seconds
    $average = 100 * ($last.cpu - $first.cpu) / $duration
    $cpuP95 = Percentile95 $cpu.ToArray()
    $memoryP95 = Percentile95 $memory.ToArray()
    [pscustomobject]@{
        seconds=[Math]::Round($duration, 2); intervals=$Intervals; sampleKinds=$kinds
        privateWorkingSetP95MiB=[Math]::Round($memoryP95, 2); cpuAverageOneCorePct=[Math]::Round($average, 3); cpuP95OneCorePct=[Math]::Round($cpuP95, 3)
        budgetPassed=($memoryP95 -le 25 -and $average -le 0.5 -and $cpuP95 -le 2)
    }
} finally {
    if (-not $app.HasExited) {
        $app.Kill()
        $app.WaitForExit()
    }
    $app.Dispose()
}
