# Starts the gateway in the current console and guarantees that closing that
# console (or killing this script) also stops the local edge proxy. The console
# host is the lifetime owner: every child process is placed in a Windows job
# object with JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, so when the last handle to the
# job goes away (the process hosting this script dies) the kernel terminates
# the whole tree. No detached proxy or gateway survives a closed window.

$ErrorActionPreference = 'Stop'
Set-Location -LiteralPath $PSScriptRoot

$exe = Join-Path $PSScriptRoot 'opencode2api.exe'
$config = Join-Path $PSScriptRoot 'config.json'
$logs = Join-Path $PSScriptRoot 'logs'
$consoleLog = Join-Path $logs 'console.log'
$edge = Join-Path $PSScriptRoot 'tools\edgeproxy.exe'
$edgePort = 29090

if (-not (Test-Path -LiteralPath $exe)) { throw "opencode2api.exe not found in $PSScriptRoot" }
if (-not (Test-Path -LiteralPath $config)) { throw "config.json not found in $PSScriptRoot" }
New-Item -ItemType Directory -Path $logs -Force | Out-Null

if (-not ('Oc2apiJob' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;

public static class Oc2apiJob
{
    [StructLayout(LayoutKind.Sequential)]
    public struct BasicLimit
    {
        public long PerProcessUserTimeLimit;
        public long PerJobUserTimeLimit;
        public uint LimitFlags;
        public UIntPtr MinimumWorkingSetSize;
        public UIntPtr MaximumWorkingSetSize;
        public uint ActiveProcessLimit;
        public UIntPtr Affinity;
        public uint PriorityClass;
        public uint SchedulingClass;
    }

    [StructLayout(LayoutKind.Sequential)]
    public struct IoCounters
    {
        public ulong ReadOperationCount;
        public ulong WriteOperationCount;
        public ulong OtherOperationCount;
        public ulong ReadTransferCount;
        public ulong WriteTransferCount;
        public ulong OtherTransferCount;
    }

    [StructLayout(LayoutKind.Sequential)]
    public struct ExtendedLimit
    {
        public BasicLimit Basic;
        public IoCounters Io;
        public UIntPtr ProcessMemoryLimit;
        public UIntPtr JobMemoryLimit;
        public UIntPtr PeakProcessMemoryUsed;
        public UIntPtr PeakJobMemoryUsed;
    }

    [DllImport("kernel32.dll", CharSet = CharSet.Unicode, SetLastError = true)]
    private static extern IntPtr CreateJobObject(IntPtr attributes, string name);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool SetInformationJobObject(IntPtr job, int infoClass, ref ExtendedLimit info, uint length);

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool AssignProcessToJobObject(IntPtr job, IntPtr process);

    [DllImport("kernel32.dll")]
    private static extern IntPtr GetCurrentProcess();

    [DllImport("kernel32.dll", SetLastError = true)]
    private static extern bool CloseHandle(IntPtr handle);

    public static bool SelfAssigned;

    public static IntPtr Create()
    {
        IntPtr job = CreateJobObject(IntPtr.Zero, null);
        if (job == IntPtr.Zero)
            throw new Win32Exception(Marshal.GetLastWin32Error());

        var info = new ExtendedLimit();
        info.Basic.LimitFlags = 0x2000; // JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
        if (!SetInformationJobObject(job, 9, ref info, (uint)Marshal.SizeOf(typeof(ExtendedLimit))))
            throw new Win32Exception(Marshal.GetLastWin32Error());

        SelfAssigned = AssignProcessToJobObject(job, GetCurrentProcess());
        return job;
    }

    public static bool Add(IntPtr job, IntPtr process)
    {
        return AssignProcessToJobObject(job, process);
    }

    public static void Close(IntPtr job)
    {
        if (job != IntPtr.Zero) CloseHandle(job);
    }
}
'@
}

$job = [Oc2apiJob]::Create()
$selfInJob = [Oc2apiJob]::SelfAssigned
if (-not $selfInJob) {
    Write-Warning "Could not join the console job; the gateway still dies with the console, the edge proxy is tracked explicitly."
}

$edgeProc = $null
try {
    # The config points at 127.0.0.1:29090. If an earlier run left the proxy
    # behind, stop it first so the one this window owns is the only listener.
    if (Test-Path -LiteralPath $edge) {
        $listener = Get-NetTCPConnection -LocalPort $edgePort -State Listen -ErrorAction SilentlyContinue |
            Select-Object -First 1
        if ($listener) {
            $owner = Get-CimInstance Win32_Process -Filter "ProcessId=$($listener.OwningProcess)" -ErrorAction SilentlyContinue
            if ($owner -and $owner.ExecutablePath -eq $edge) {
                Stop-Process -Id $owner.ProcessId -Force -ErrorAction SilentlyContinue
                Start-Sleep -Milliseconds 500
            } else {
                Write-Warning "Port $edgePort is held by another program; leaving it alone."
            }
        }

        $edgeOut = Join-Path $logs 'edgeproxy.out.log'
        $edgeErr = Join-Path $logs 'edgeproxy.err.log'
        $edgeProc = Start-Process -FilePath $edge `
            -WorkingDirectory $PSScriptRoot `
            -WindowStyle Hidden `
            -RedirectStandardOutput $edgeOut `
            -RedirectStandardError $edgeErr `
            -PassThru
        if (-not $selfInJob) {
            [void][Oc2apiJob]::Add($job, $edgeProc.Handle)
        }
        Write-Host "Edge proxy: PID $($edgeProc.Id) on 127.0.0.1:$edgePort"
    } else {
        Write-Host "Edge proxy: tools\edgeproxy.exe not found; using direct/proxy fallbacks."
    }

    Write-Host "Gateway: starting; close this window or press Ctrl+C to stop everything."
    Write-Host ""

    # Tee keeps the console live and mirrors it to logs\console.log. The
    # gateway inherits this console and this job, so a window close stops it
    # directly and the job covers anything that outlives it.
    & $exe -config $config 2>&1 | Tee-Object -FilePath $consoleLog
    $exitCode = $LASTEXITCODE
}
finally {
    if ($edgeProc) {
        try {
            if (-not $edgeProc.HasExited) {
                Stop-Process -Id $edgeProc.Id -Force -ErrorAction SilentlyContinue
            }
        } catch { }
    }
    if (-not $selfInJob) {
        # Safe only when this process is not a job member: closing the handle
        # would otherwise terminate the shell before it can report the exit.
        [Oc2apiJob]::Close($job)
    }
}

exit $exitCode
