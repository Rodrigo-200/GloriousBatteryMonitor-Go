using Avalonia;
using Avalonia.Controls;
using Avalonia.Input;
using Avalonia.Interactivity;
using Avalonia.Platform;
using Avalonia.Threading;
using GBM.Core.Services;
using GBM.Desktop.Services;
using GBM.Desktop.ViewModels;
using System.Runtime.InteropServices;

namespace GBM.Desktop.Views;

public partial class BatteryWidgetWindow : Window
{
    private static readonly nint HwndTopmost = new(-1);
    private const uint SwpNoSize = 0x0001;
    private const uint SwpNoMove = 0x0002;
    private const uint SwpNoActivate = 0x0010;
    private bool _allowClose;
    private bool _positioned;
    private bool _restoringPosition;
    private ISettingsService? _settingsService;
    private readonly DispatcherTimer _positionSaveTimer;

    public BatteryWidgetWindow()
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
            if (!_restoringPosition && _positioned && _settingsService != null)
            {
                _positionSaveTimer.Stop();
                _positionSaveTimer.Start();
            }
        };

        var dragRegion = this.FindControl<Control>("DragRegion");
        if (dragRegion != null)
        {
            dragRegion.PointerReleased += (_, _) => SavePosition();
            dragRegion.PointerPressed += (_, e) =>
            {
                if (e.GetCurrentPoint(this).Properties.IsLeftButtonPressed)
                {
                    if (e.ClickCount >= 2)
                    {
                        e.Handled = true;
                        if (Application.Current is App app)
                            app.ShowMainWindowFromTray();
                        return;
                    }

                    BeginMoveDrag(e);
                }
            };
        }

        Opened += (_, _) =>
        {
            RestoreOrPlace();
            KeepAboveTaskbar();
        };
        Deactivated += (_, _) =>
            Dispatcher.UIThread.Post(() =>
            {
                if (IsVisible && !_allowClose)
                    KeepAboveTaskbar();
            }, DispatcherPriority.Background);
    }

    public void ClosePermanently()
    {
        _allowClose = true;
        Close();
    }

    public void ConfigurePlacement(ISettingsService settingsService) => _settingsService = settingsService;

    private void OnWidgetSettingsClicked(object? sender, RoutedEventArgs e)
    {
        if (Application.Current is App app)
            app.ShowSettingsFromTray();
    }

    private void OnWidgetOpenAppClicked(object? sender, RoutedEventArgs e)
    {
        if (Application.Current is App app)
            app.ShowMainWindowFromTray();
    }

    // Persists ShowBatteryWidget=false; App hides the window when the setting changes.
    private void OnWidgetHideClicked(object? sender, RoutedEventArgs e) =>
        (DataContext as MainViewModel)?.HideBatteryWidget();

    protected override void OnClosing(WindowClosingEventArgs e)
    {
        if (_allowClose || e.CloseReason is WindowCloseReason.ApplicationShutdown or WindowCloseReason.OSShutdown)
        {
            _positionSaveTimer.Stop();
            SavePosition();
        }
        else
        {
            e.Cancel = true;
        }

        base.OnClosing(e);
    }

    private void RestoreOrPlace()
    {
        if (_positioned)
            return;

        if (_settingsService != null)
        {
            // Older builds saved the widget at the lower-right screen edge (v1) or as a
            // fixed 156x24 window without the shadow margin (v2). Re-place it once on upgrade.
            if (_settingsService.Current.BatteryWidgetPositionVersion < 3)
            {
                var migrated = _settingsService.Current.Clone();
                migrated.BatteryWidgetX = null;
                migrated.BatteryWidgetY = null;
                migrated.BatteryWidgetPositionVersion = 3;
                _settingsService.Save(migrated);
            }

            var savedPosition = WindowPositionHelper.GetVisiblePosition(
                this, _settingsService.Current.BatteryWidgetX, _settingsService.Current.BatteryWidgetY,
                includeTaskbar: true);
            if (savedPosition != null)
            {
                _restoringPosition = true;
                Position = savedPosition.Value;
                _restoringPosition = false;
                _positioned = true;
                return;
            }
        }

        var screen = Screens.Primary;
        if (screen == null)
            return;

        var workArea = screen.WorkingArea;
        var scale = screen.Scaling;
        var widgetWidth = (int)Math.Round(Bounds.Width * scale);
        var widgetHeight = (int)Math.Round(Bounds.Height * scale);
        if (OperatingSystem.IsWindows() && TryPlaceLeftOfNotificationArea(widgetWidth, widgetHeight, scale, out var taskbarPosition))
        {
            PlaceWithinScreen(taskbarPosition);
            return;
        }

        var taskbarHeight = screen.Bounds.Bottom - workArea.Bottom;
        PixelPoint position;
        if (OperatingSystem.IsWindows() && taskbarHeight > 0)
        {
            // Fallback when Explorer does not expose the notification area child window.
            var reservedTrayWidth = (int)Math.Round(210 * scale);
            position = new PixelPoint(
                workArea.Right - widgetWidth - reservedTrayWidth,
                workArea.Bottom + Math.Max(0, (taskbarHeight - widgetHeight) / 2));
        }
        else
        {
            var margin = (int)Math.Round(18 * scale);
            position = new PixelPoint(workArea.Right - widgetWidth - margin, workArea.Bottom - widgetHeight - margin);
        }
        PlaceWithinScreen(position);
    }

    private void PlaceWithinScreen(PixelPoint position)
    {
        Position = WindowPositionHelper.GetVisiblePosition(this, position.X, position.Y, includeTaskbar: true) ?? position;
        _positioned = true;
    }

    private static bool TryPlaceLeftOfNotificationArea(int widgetWidth, int widgetHeight, double scale, out PixelPoint position)
    {
        position = default;
        var taskbar = FindWindow("Shell_TrayWnd", null);
        var notificationArea = taskbar == nint.Zero
            ? nint.Zero
            : FindWindowEx(taskbar, nint.Zero, "TrayNotifyWnd", null);
        if (notificationArea == nint.Zero || !GetWindowRect(notificationArea, out var rect))
            return false;

        var gap = (int)Math.Round(8 * scale);
        position = new PixelPoint(
            rect.Left - widgetWidth - gap,
            rect.Top + Math.Max(0, (rect.Bottom - rect.Top - widgetHeight) / 2));
        return true;
    }

    [StructLayout(LayoutKind.Sequential)]
    private struct NativeRect
    {
        public int Left;
        public int Top;
        public int Right;
        public int Bottom;
    }

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    private static extern nint FindWindow(string? className, string? windowName);

    [DllImport("user32.dll", CharSet = CharSet.Unicode)]
    private static extern nint FindWindowEx(nint parent, nint childAfter, string? className, string? windowName);

    [DllImport("user32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool GetWindowRect(nint hWnd, out NativeRect rect);

    private void SavePosition()
    {
        if (_settingsService == null || !_positioned)
            return;

        var current = _settingsService.Current;
        if (current.BatteryWidgetX == Position.X && current.BatteryWidgetY == Position.Y)
            return;

        var settings = current.Clone();
        settings.BatteryWidgetX = Position.X;
        settings.BatteryWidgetY = Position.Y;
        _settingsService.Save(settings);
    }

    private void KeepAboveTaskbar()
    {
        if (!OperatingSystem.IsWindows())
            return;

        var handle = TryGetPlatformHandle()?.Handle ?? nint.Zero;
        if (handle == nint.Zero)
            return;

        SetWindowPos(handle, HwndTopmost, 0, 0, 0, 0,
            SwpNoMove | SwpNoSize | SwpNoActivate);
    }

    [DllImport("user32.dll", SetLastError = true)]
    [return: MarshalAs(UnmanagedType.Bool)]
    private static extern bool SetWindowPos(
        nint hWnd,
        nint hWndInsertAfter,
        int x,
        int y,
        int cx,
        int cy,
        uint flags);
}
