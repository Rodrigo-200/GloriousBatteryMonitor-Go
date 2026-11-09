using Avalonia.Controls;
using Avalonia.Platform.Storage;
using GBM.Desktop.ViewModels;

namespace GBM.Desktop.Views;

public partial class SettingsView : UserControl
{
    public SettingsView()
    {
        InitializeComponent();
    }

    private async void OnChooseCriticalSoundClicked(object? sender, Avalonia.Interactivity.RoutedEventArgs e)
    {
        if (DataContext is not MainViewModel viewModel || TopLevel.GetTopLevel(this) is not { } topLevel)
            return;

        var files = await topLevel.StorageProvider.OpenFilePickerAsync(new FilePickerOpenOptions
        {
            Title = "Choose a battery alert sound",
            AllowMultiple = false,
            FileTypeFilter = new[]
            {
                new FilePickerFileType("Audio files")
                {
                    Patterns = new[] { "*.wav", "*.mp3", "*.flac" }
                }
            }
        });

        if (files.Count > 0)
            viewModel.CriticalBatterySoundPath = files[0].Path.LocalPath;
    }

    private void OnClearCriticalSoundClicked(object? sender, Avalonia.Interactivity.RoutedEventArgs e)
    {
        if (DataContext is MainViewModel viewModel)
            viewModel.CriticalBatterySoundPath = string.Empty;
    }
}
