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
        ["BatteryAlerts"] = "Get battery alerts", ["Sound10"] = "Sound at 10%", ["Choose"] = "Choose…",
        ["None"] = "None", ["TestSound"] = "Test sound", ["RefreshRate"] = "Refresh rate (s)",
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
        ["TimeToFull"] = "Time to Full"
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
        ["BatteryAlerts"] = "Уведомлять об уровне заряда", ["Sound10"] = "Звук при 10% заряда",
        ["Choose"] = "Выбрать…", ["None"] = "Нет", ["TestSound"] = "Проверить звук",
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
        ["TimeToFull"] = "До полного заряда"
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
