using System.Diagnostics;
using System.IO.Pipes;
using System.Runtime.InteropServices;

namespace GBM.Desktop.Services;

internal sealed class SingleInstanceCoordinator : IDisposable
{
    private const string ActivationCommand = "activate";
    private static readonly TimeSpan RetryDelay = TimeSpan.FromMilliseconds(100);

    private readonly Mutex _mutex;
    private readonly string _pipeName;
    private readonly CancellationTokenSource _shutdown = new();
    private readonly object _activationLock = new();
    private readonly Task? _listenerTask;
    private Action? _activationHandler;
    private bool _activationPending;
    private bool _disposed;

    private SingleInstanceCoordinator(string mutexName, string pipeName)
    {
        _pipeName = pipeName;
        // Keeping the named handle open is enough to identify the first process.
        // Avoid taking ownership so creation/disposal is not thread-affine.
        _mutex = new Mutex(false, mutexName, out bool isPrimaryInstance);
        IsPrimaryInstance = isPrimaryInstance;

        if (IsPrimaryInstance)
            _listenerTask = ListenForActivationAsync(_shutdown.Token);
    }

    public bool IsPrimaryInstance { get; }

    public static SingleInstanceCoordinator CreateForCurrentSession()
    {
        int sessionId = Process.GetCurrentProcess().SessionId;
        return new SingleInstanceCoordinator(
            "GloriousBatteryMonitor_SingleInstance",
            $"GloriousBatteryMonitor_Activation_{sessionId}");
    }

    internal static SingleInstanceCoordinator CreateForTesting(
        string mutexName,
        string pipeName)
    {
        return new SingleInstanceCoordinator(mutexName, pipeName);
    }

    public void RegisterActivationHandler(Action handler)
    {
        ArgumentNullException.ThrowIfNull(handler);

        bool invokePending;
        lock (_activationLock)
        {
            _activationHandler = handler;
            invokePending = _activationPending;
            _activationPending = false;
        }

        if (invokePending)
            handler();
    }

    public async Task<bool> NotifyPrimaryInstanceAsync(TimeSpan timeout)
    {
        if (IsPrimaryInstance || timeout <= TimeSpan.Zero)
            return false;

        try
        {
            using var timeoutSource = new CancellationTokenSource(timeout);
            using var pipe = new NamedPipeClientStream(
                ".",
                _pipeName,
                PipeDirection.InOut,
                PipeOptions.Asynchronous | PipeOptions.CurrentUserOnly);

            await pipe.ConnectAsync(timeoutSource.Token).ConfigureAwait(false);

            using var reader = new StreamReader(pipe, leaveOpen: true);
            using var writer = new StreamWriter(pipe, leaveOpen: true) { AutoFlush = true };

            string? processIdText = await reader
                .ReadLineAsync(timeoutSource.Token)
                .ConfigureAwait(false);

            if (uint.TryParse(processIdText, out uint processId))
                _ = AllowSetForegroundWindow(processId);

            await writer.WriteLineAsync(ActivationCommand).ConfigureAwait(false);
            return true;
        }
        catch (OperationCanceledException)
        {
            return false;
        }
        catch (IOException)
        {
            return false;
        }
        catch (UnauthorizedAccessException)
        {
            return false;
        }
    }

    private async Task ListenForActivationAsync(CancellationToken cancellationToken)
    {
        while (!cancellationToken.IsCancellationRequested)
        {
            try
            {
                await using var pipe = new NamedPipeServerStream(
                    _pipeName,
                    PipeDirection.InOut,
                    1,
                    PipeTransmissionMode.Byte,
                    PipeOptions.Asynchronous | PipeOptions.CurrentUserOnly);

                await pipe.WaitForConnectionAsync(cancellationToken).ConfigureAwait(false);

                using var reader = new StreamReader(pipe, leaveOpen: true);
                using var writer = new StreamWriter(pipe, leaveOpen: true) { AutoFlush = true };

                await writer
                    .WriteLineAsync(Environment.ProcessId.ToString())
                    .ConfigureAwait(false);

                string? command = await reader
                    .ReadLineAsync(cancellationToken)
                    .ConfigureAwait(false);

                if (string.Equals(command, ActivationCommand, StringComparison.Ordinal))
                    RequestActivation();
            }
            catch (OperationCanceledException) when (cancellationToken.IsCancellationRequested)
            {
                break;
            }
            catch (IOException) when (!cancellationToken.IsCancellationRequested)
            {
                await DelayAfterFailureAsync(cancellationToken).ConfigureAwait(false);
            }
            catch (UnauthorizedAccessException) when (!cancellationToken.IsCancellationRequested)
            {
                await DelayAfterFailureAsync(cancellationToken).ConfigureAwait(false);
            }
        }
    }

    private void RequestActivation()
    {
        Action? handler;
        lock (_activationLock)
        {
            handler = _activationHandler;
            if (handler == null)
            {
                _activationPending = true;
                return;
            }
        }

        handler();
    }

    private static async Task DelayAfterFailureAsync(CancellationToken cancellationToken)
    {
        try
        {
            await Task.Delay(RetryDelay, cancellationToken).ConfigureAwait(false);
        }
        catch (OperationCanceledException)
        {
            // Normal during application shutdown.
        }
    }

    public void Dispose()
    {
        if (_disposed)
            return;

        _disposed = true;
        _shutdown.Cancel();

        if (_listenerTask != null)
        {
            try
            {
                _listenerTask.GetAwaiter().GetResult();
            }
            catch (OperationCanceledException)
            {
                // Normal during application shutdown.
            }
        }

        _mutex.Dispose();
        _shutdown.Dispose();
    }

    [DllImport("user32.dll")]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool AllowSetForegroundWindow(uint processId);
}
