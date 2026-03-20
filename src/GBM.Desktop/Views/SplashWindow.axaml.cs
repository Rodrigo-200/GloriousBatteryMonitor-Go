using Avalonia.Controls;
using Avalonia.Interactivity;
using Avalonia.Threading;
using GBM.Core.Models;
using GBM.Core.Services;

namespace GBM.Desktop.Views;

public partial class SplashWindow : Window
{
    private TaskCompletionSource<bool>? _updateDecision;

    public SplashWindow()
    {
        InitializeComponent();
    }

    public void SetStatus(string text)
    {
        Dispatcher.UIThread.Post(() =>
        {
            StatusText.Text = text;
        });
    }

    /// <summary>
    /// Shows "Update available: vX.Y.Z" with Update/Skip buttons.
    /// Returns true if user chose to update, false if skipped.
    /// </summary>
    public Task<bool> ShowUpdatePromptAsync(string version)
    {
        _updateDecision = new TaskCompletionSource<bool>();

        Dispatcher.UIThread.Post(() =>
        {
            StatusText.Text = $"Update available: v{version}";
            IndeterminateProgress.IsVisible = false;
            UpdateButtonsPanel.IsVisible = true;

            UpdateButton.Click += OnUpdateClicked;
            SkipButton.Click += OnSkipClicked;
        });

        return _updateDecision.Task;
    }

    /// <summary>
    /// Switches UI to show a determinate download progress bar.
    /// </summary>
    public void ShowDownloadProgress()
    {
        Dispatcher.UIThread.Post(() =>
        {
            UpdateButtonsPanel.IsVisible = false;
            IndeterminateProgress.IsVisible = false;
            DownloadPanel.IsVisible = true;
            StatusText.Text = "Downloading update...";
        });
    }

    /// <summary>
    /// Updates the download progress bar (0-100).
    /// </summary>
    public void UpdateProgress(int percent)
    {
        Dispatcher.UIThread.Post(() =>
        {
            DownloadProgress.Value = percent;
            DownloadPercentText.Text = $"{percent}%";
        });
    }

    /// <summary>
    /// Shows the device connection phase on the splash screen.
    /// Must be called AFTER vm.InitializeAsync() so the polling loop is
    /// already running and events are being fired.
    /// Returns true if a device connected, false if skipped or timed out.
    /// </summary>
    public async Task<bool> ShowDeviceSearchAsync(
        IBatteryMonitorService monitorService,
        TimeSpan timeout)
    {
        var tcs = new TaskCompletionSource<bool>(
            TaskCreationOptions.RunContinuationsAsynchronously);
        bool advanced = false;

        void OnBatteryStateChanged(BatteryState state)
        {
            if (state.Connection == ConnectionState.Connected ||
                state.Connection == ConnectionState.Sleeping ||
                state.Connection == ConnectionState.LastKnown)
            {
                if (!advanced)
                {
                    advanced = true;
                    tcs.TrySetResult(true);
                }
            }
        }

        void OnProbeStatusChanged(string status)
        {
            Dispatcher.UIThread.Post(() =>
            {
                var txt = this.FindControl<TextBlock>("DeviceStatusText");
                if (txt != null) txt.Text = status;
            });
        }

        monitorService.BatteryStateChanged += OnBatteryStateChanged;
        monitorService.ProbeStatusChanged += OnProbeStatusChanged;

        await Dispatcher.UIThread.InvokeAsync(() =>
        {
            var panel = this.FindControl<StackPanel>("DeviceSearchPanel");
            if (panel != null) panel.IsVisible = true;
        });

        // Show "Continue anyway" button after 5 seconds if not yet connected
        _ = Task.Run(async () =>
        {
            await Task.Delay(TimeSpan.FromSeconds(5));
            if (!advanced)
            {
                await Dispatcher.UIThread.InvokeAsync(() =>
                {
                    var btn = this.FindControl<Button>("SkipDeviceButton");
                    if (btn != null) btn.IsVisible = true;
                });
            }
        });

        // Wire Skip button on UI thread
        await Dispatcher.UIThread.InvokeAsync(() =>
        {
            var btn = this.FindControl<Button>("SkipDeviceButton");
            if (btn != null)
            {
                btn.Click += (_, _) =>
                {
                    if (!advanced)
                    {
                        advanced = true;
                        tcs.TrySetResult(false);
                    }
                };
            }
        });

        // Wait for connection, skip, or timeout
        await Task.WhenAny(tcs.Task, Task.Delay(timeout));

        // Always unsubscribe
        monitorService.BatteryStateChanged -= OnBatteryStateChanged;
        monitorService.ProbeStatusChanged -= OnProbeStatusChanged;

        bool connected = tcs.Task.IsCompleted && tcs.Task.Result;

        if (!connected)
        {
            await Dispatcher.UIThread.InvokeAsync(() =>
            {
                var txt = this.FindControl<TextBlock>("DeviceStatusText");
                if (txt != null) txt.Text = "Mouse not found — you can reconnect later";
                var btn = this.FindControl<Button>("SkipDeviceButton");
                if (btn != null) btn.IsVisible = false;
            });
            await Task.Delay(TimeSpan.FromSeconds(1));
        }

        return connected;
    }

    private void OnUpdateClicked(object? sender, RoutedEventArgs e)
    {
        UpdateButton.Click -= OnUpdateClicked;
        SkipButton.Click -= OnSkipClicked;
        _updateDecision?.TrySetResult(true);
    }

    private void OnSkipClicked(object? sender, RoutedEventArgs e)
    {
        UpdateButton.Click -= OnUpdateClicked;
        SkipButton.Click -= OnSkipClicked;
        _updateDecision?.TrySetResult(false);
    }
}
