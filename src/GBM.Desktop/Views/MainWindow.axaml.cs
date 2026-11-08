using Avalonia.Controls;
using Avalonia.Input;
using Avalonia.Threading;
using GBM.Core.Services;
using GBM.Desktop.Services;
using GBM.Desktop.ViewModels;

namespace GBM.Desktop.Views;

public partial class MainWindow : Window
{
    private ISettingsService? _settingsService;
    private readonly DispatcherTimer _positionSaveTimer;
    private bool _restoringPosition;
    private bool _hasOpened;
    private MainViewModel? _observedViewModel;

    public MainWindow()
    {
        InitializeComponent();

        _positionSaveTimer = new DispatcherTimer { Interval = TimeSpan.FromMilliseconds(350) };
        _positionSaveTimer.Tick += (_, _) =>
        {
            _positionSaveTimer.Stop();
            SavePosition();
        };
        PositionChanged += (_, _) =>
        {
            if (!_restoringPosition && WindowState == WindowState.Normal && _settingsService != null)
            {
                _positionSaveTimer.Stop();
                _positionSaveTimer.Start();
            }
        };
        Deactivated += (_, _) => SavePosition();
        Opened += (_, _) =>
        {
            _hasOpened = true;
            RestorePosition();
            UpdateWindowSize();
        };
        DataContextChanged += (_, _) => UpdateWindowSize();

        var titleBar = this.FindControl<Border>("TitleBar");
        if (titleBar != null)
        {
            titleBar.PointerReleased += (_, _) => SavePosition();
            titleBar.PointerPressed += (s, e) =>
            {
                if (e.GetCurrentPoint(this).Properties.IsLeftButtonPressed)
                    BeginMoveDrag(e);
            };
        }
    }

    public void ConfigurePlacement(ISettingsService settingsService) => _settingsService = settingsService;

    private void UpdateWindowSize()
    {
        if (DataContext is not MainViewModel vm) return;
        if (!ReferenceEquals(_observedViewModel, vm))
        {
            _observedViewModel = vm;
            vm.PropertyChanged += (_, e) =>
            {
                if (e.PropertyName == nameof(MainViewModel.IsSettingsOpen))
                    Dispatcher.UIThread.Post(UpdateWindowSize);
            };
        }
        Width = vm.IsSettingsOpen ? 520 : 420;
        Height = vm.IsSettingsOpen ? 720 : 640;
        MinWidth = vm.IsSettingsOpen ? 420 : 380;
        MinHeight = vm.IsSettingsOpen ? 500 : 560;
    }

    private void RestorePosition()
    {
        if (_settingsService == null)
            return;

        var position = WindowPositionHelper.GetVisiblePosition(
            this, _settingsService.Current.MainWindowX, _settingsService.Current.MainWindowY);
        if (position == null)
            return;

        _restoringPosition = true;
        Position = position.Value;
        _restoringPosition = false;
    }

    private void SavePosition()
    {
        if (_settingsService == null || !_hasOpened || WindowState != WindowState.Normal)
            return;

        var settings = _settingsService.Current.Clone();
        settings.MainWindowX = Position.X;
        settings.MainWindowY = Position.Y;
        _settingsService.Save(settings);
    }

    public void SavePlacement() => SavePosition();

    protected override void OnClosing(WindowClosingEventArgs e)
    {
        // Minimize to tray instead of closing
        SavePosition();
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
