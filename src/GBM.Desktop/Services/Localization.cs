using Avalonia;
using System.Globalization;

namespace GBM.Desktop.Services;

public static class Localization
{
    private static readonly Dictionary<string, string> English = new()
    {
        ["Settings"] = "Settings", ["General"] = "GENERAL", ["Advanced"] = "ADVANCED",
        ["StartWithOS"] = "Start with Windows", ["AutoStart"] = "Launch automatically on startup",
        ["StartMinimized"] = "Start minimized", ["StartTray"] = "Start in the system tray",
        ["Theme"] = "Theme", ["Appearance"] = "Application appearance", ["System"] = "System",
        ["Dark"] = "Dark", ["Light"] = "Light", ["Language"] = "Language",
        ["ChooseLanguage"] = "Choose the interface language", ["LanguageSystem"] = "System language",
        ["English"] = "English", ["Russian"] = "Русский", ["Notifications"] = "Notifications",
        ["BatteryAlerts"] = "Get battery alerts", ["CriticalSound"] = "Critical battery sound", ["Choose"] = "Choose…",
        ["TestSound"] = "Test sound", ["RefreshRate"] = "Refresh rate (s)",
        ["LowBattery"] = "Low battery (%)", ["CriticalBattery"] = "Critical battery (%)",
        ["Cooldown"] = "Notification cooldown (min)", ["Debug"] = "Debug logging",
        ["DebugDescription"] = "Write verbose logs (restart required)", ["Developer"] = "DEVELOPER TOOLS",
        ["ScanHID"] = "Scan HID", ["Capture"] = "Capture", ["Copy"] = "Copy",
        ["Diagnostics"] = "Diagnostics", ["Updates"] = "UPDATES", ["Beta"] = "Beta updates",
        ["BetaDescription"] = "Opt in to pre-release builds.", ["CheckUpdates"] = "Check for updates",
        ["Cancel"] = "Cancel", ["Save"] = "Save", ["MainWindow"] = "Main window",
        ["Widget"] = "Battery widget", ["Rescan"] = "Rescan", ["Quit"] = "Quit",
        ["Connected"] = "Connected", ["Disconnected"] = "Disconnected", ["Connecting"] = "Connecting",
        ["LastKnown"] = "Last known", ["Sleeping"] = "Sleeping", ["Charging"] = "Charging",
        ["MouseNotFound"] = "Mouse not found", ["TimeRemaining"] = "Estimated remaining time",
        ["ChargedTo"] = "CHARGED TO", ["FullChargeLasts"] = "FULL CHARGE LASTS",
        ["LastCharge"] = "LAST CHARGE", ["LastSync"] = "LAST SYNC", ["JustNow"] = "Just now", ["Dpi"] = "DPI",
        ["TimeToFull"] = "Time to Full",
        ["TimeRemainingShort"] = "TIME REMAINING", ["TimeToFullShort"] = "TIME TO FULL",
        ["Alerts"] = "ALERTS", ["TrayAndWidget"] = "TRAY & WIDGET",
        ["TrayPercentage"] = "Tray percentage", ["TrayPercentageDescription"] = "Show the number on the tray icon",
        ["DesktopWidget"] = "Desktop widget", ["DesktopWidgetDescription"] = "Floating battery + DPI pill, drag to place",
        ["ShowDpi"] = "Show DPI", ["ShowDpiDescription"] = "Active stage on dashboard and widget",
        ["CriticalSoundDescription"] = "Plays once when the mouse drops to {0}%",
        ["NoSoundSelected"] = "No sound selected", ["RemoveSound"] = "Remove sound",
        ["OpenApp"] = "Open Battery Monitor", ["HideWidget"] = "Hide widget", ["MouseAsleep"] = "Mouse asleep",
        ["DpiTooltipFactory"] = "Stage {0}: {1}×{2} DPI, {3}. Factory stage colour.",
        ["DpiColorYellow"] = "yellow", ["DpiColorBlue"] = "blue", ["DpiColorOrange"] = "orange",
        ["DpiColorGreen"] = "green", ["DpiColorUnknown"] = "default colour unknown",
        ["Over30Days"] = "~30d+", ["ToastSaved"] = "Settings saved successfully",
        ["ToastSoundFailed"] = "Could not play the selected sound file", ["ToastScanComplete"] = "Scan complete",
        ["ToastCopied"] = "Copied to clipboard", ["ToastDiagnostics"] = "Diagnostics captured",
        ["ToastUpdateReady"] = "Update downloaded. Restart when ready.",
        ["ChooseSoundTitle"] = "Choose a battery alert sound", ["AudioFiles"] = "Audio files"
    };

    private static readonly Dictionary<string, string> Russian = new()
    {
        ["Settings"] = "Настройки", ["General"] = "ОБЩИЕ", ["Advanced"] = "ДОПОЛНИТЕЛЬНО",
        ["StartWithOS"] = "Запускать вместе с Windows", ["AutoStart"] = "Запускать автоматически при входе",
        ["StartMinimized"] = "Запускать свёрнутым", ["StartTray"] = "Запускать в системном трее",
        ["Theme"] = "Тема", ["Appearance"] = "Внешний вид приложения", ["System"] = "Системная",
        ["Dark"] = "Тёмная", ["Light"] = "Светлая", ["Language"] = "Язык",
        ["ChooseLanguage"] = "Выберите язык интерфейса", ["LanguageSystem"] = "Язык системы",
        ["English"] = "English", ["Russian"] = "Русский", ["Notifications"] = "Уведомления",
        ["BatteryAlerts"] = "Уведомлять об уровне заряда", ["CriticalSound"] = "Звук при критическом заряде",
        ["Choose"] = "Выбрать…", ["TestSound"] = "Проверить звук",
        ["RefreshRate"] = "Интервал опроса (с)", ["LowBattery"] = "Низкий заряд (%)",
        ["CriticalBattery"] = "Критический заряд (%)", ["Cooldown"] = "Интервал между уведомлениями (мин)",
        ["Debug"] = "Подробный журнал", ["DebugDescription"] = "Записывать подробные данные (нужен перезапуск)",
        ["Developer"] = "ИНСТРУМЕНТЫ РАЗРАБОТЧИКА", ["ScanHID"] = "Поиск HID", ["Capture"] = "Захват",
        ["Copy"] = "Копировать", ["Diagnostics"] = "Диагностика", ["Updates"] = "ОБНОВЛЕНИЯ",
        ["Beta"] = "Бета-обновления", ["BetaDescription"] = "Получать предварительные версии.",
        ["CheckUpdates"] = "Проверить обновления", ["Cancel"] = "Отмена", ["Save"] = "Сохранить",
        ["MainWindow"] = "Главное окно", ["Widget"] = "Виджет заряда", ["Rescan"] = "Повторить поиск",
        ["Quit"] = "Выход", ["Connected"] = "Подключена", ["Disconnected"] = "Не подключена",
        ["Connecting"] = "Подключение", ["LastKnown"] = "Последнее состояние", ["Sleeping"] = "Спит",
        ["Charging"] = "Заряжается", ["MouseNotFound"] = "Мышь не найдена",
        ["TimeRemaining"] = "Примерное время работы", ["ChargedTo"] = "ЗАРЯЖЕНО ДО",
        ["FullChargeLasts"] = "ПОЛНЫЙ ЗАРЯД ДЕРЖИТ", ["LastCharge"] = "ПОСЛЕДНЯЯ ЗАРЯДКА",
        ["LastSync"] = "СИНХРОНИЗАЦИЯ", ["JustNow"] = "Только что", ["Dpi"] = "DPI",
        ["TimeToFull"] = "До полного заряда",
        ["TimeRemainingShort"] = "ОСТАЛОСЬ", ["TimeToFullShort"] = "ДО ПОЛНОГО",
        ["Alerts"] = "ОПОВЕЩЕНИЯ", ["TrayAndWidget"] = "ТРЕЙ И ВИДЖЕТ",
        ["TrayPercentage"] = "Процент в трее", ["TrayPercentageDescription"] = "Показывать число на значке в трее",
        ["DesktopWidget"] = "Виджет на рабочем столе", ["DesktopWidgetDescription"] = "Плавающая панель заряда и DPI, можно перетаскивать",
        ["ShowDpi"] = "Показывать DPI", ["ShowDpiDescription"] = "Активная ступень на главном экране и в виджете",
        ["CriticalSoundDescription"] = "Играет один раз, когда заряд падает до {0}%",
        ["NoSoundSelected"] = "Звук не выбран", ["RemoveSound"] = "Убрать звук",
        ["OpenApp"] = "Открыть Battery Monitor", ["HideWidget"] = "Скрыть виджет", ["MouseAsleep"] = "Мышь спит",
        ["DpiTooltipFactory"] = "Ступень {0}: {1}×{2} DPI, {3}. Цвет взят из заводской схемы.",
        ["DpiColorYellow"] = "жёлтый", ["DpiColorBlue"] = "синий", ["DpiColorOrange"] = "оранжевый",
        ["DpiColorGreen"] = "зелёный", ["DpiColorUnknown"] = "цвет по умолчанию неизвестен",
        ["Over30Days"] = "~30 дней+", ["ToastSaved"] = "Настройки сохранены",
        ["ToastSoundFailed"] = "Не удалось воспроизвести выбранный звук", ["ToastScanComplete"] = "Поиск завершён",
        ["ToastCopied"] = "Скопировано в буфер обмена", ["ToastDiagnostics"] = "Диагностика собрана",
        ["ToastUpdateReady"] = "Обновление загружено. Перезапустите, когда будет удобно.",
        ["ChooseSoundTitle"] = "Выберите звук для оповещения о заряде", ["AudioFiles"] = "Аудиофайлы"
    };

    public static string CurrentLanguage { get; private set; } = "en";
    public static event Action? LanguageChanged;

    public static void Apply(string? preference)
    {
        var language = Normalize(preference);
        CurrentLanguage = language == "system"
            ? CultureInfo.CurrentUICulture.TwoLetterISOLanguageName.Equals("ru", StringComparison.OrdinalIgnoreCase) ? "ru" : "en"
            : language;
        if (Application.Current is { } app)
        {
            var dictionary = CurrentLanguage == "ru" ? Russian : English;
            foreach (var pair in dictionary)
                app.Resources[pair.Key] = pair.Value;
        }
        LanguageChanged?.Invoke();
    }

    public static string Text(string key) => (CurrentLanguage == "ru" ? Russian : English).GetValueOrDefault(key, key);

    public static string Normalize(string? language) => language?.Trim().ToLowerInvariant() switch
    {
        "ru" or "russian" => "ru",
        "en" or "english" => "en",
        _ => "system"
    };
}
