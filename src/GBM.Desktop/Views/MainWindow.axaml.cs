using Avalonia.Controls;
using Avalonia.Input;
using GBM.Desktop.ViewModels;

namespace GBM.Desktop.Views;

public partial class MainWindow : Window
{
    public MainWindow()
    {
        InitializeComponent();

        var titleBar = this.FindControl<Border>("TitleBar");
        if (titleBar != null)
        {
            titleBar.PointerPressed += (s, e) =>
            {
                if (e.GetCurrentPoint(this).Properties.IsLeftButtonPressed)
                    BeginMoveDrag(e);
            };
        }
    }

    protected override void OnClosing(WindowClosingEventArgs e)
    {
        // Minimize to tray instead of closing
        e.Cancel = true;
        Hide();
        base.OnClosing(e);
    }

    protected override void OnKeyDown(KeyEventArgs e)
    {
        base.OnKeyDown(e);

        if (e.Key == Key.Escape && DataContext is MainViewModel vm && vm.IsSettingsOpen)
        {
            vm.CloseSettingsCommand.Execute(null);
            e.Handled = true;
        }
        else if (e.Key == Key.Q && e.KeyModifiers.HasFlag(KeyModifiers.Control))
        {
            // Full quit
            if (Avalonia.Application.Current?.ApplicationLifetime is
                Avalonia.Controls.ApplicationLifetimes.IClassicDesktopStyleApplicationLifetime desktop)
            {
                desktop.Shutdown();
            }
            e.Handled = true;
        }
    }
}
