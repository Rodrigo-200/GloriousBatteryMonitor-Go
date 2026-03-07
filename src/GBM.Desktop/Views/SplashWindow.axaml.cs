using Avalonia.Controls;
using Avalonia.Interactivity;
using Avalonia.Threading;

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
