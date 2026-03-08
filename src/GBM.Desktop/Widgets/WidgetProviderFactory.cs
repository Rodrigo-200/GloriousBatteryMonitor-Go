using System.Runtime.InteropServices;
using Microsoft.Windows.Widgets.Providers;
using WinRT;

namespace GBM.Desktop.Widgets;

/// <summary>
/// COM class factory for the widget provider.
/// Windows calls CoCreateInstance with our CLSID, which invokes this factory
/// to create a BatteryWidgetProvider instance.
/// </summary>
[ComVisible(true)]
internal sealed class WidgetProviderFactory : IClassFactory
{
    public int CreateInstance(IntPtr pUnkOuter, ref Guid riid, out IntPtr ppvObject)
    {
        ppvObject = IntPtr.Zero;

        if (pUnkOuter != IntPtr.Zero)
        {
            Marshal.ThrowExceptionForHR(CLASS_E_NOAGGREGATION);
        }

        // Return a WinRT-marshaled IWidgetProvider. The COM runtime will
        // QueryInterface for the specific riid the caller needs.
        ppvObject = MarshalInspectable<IWidgetProvider>.FromManaged(new BatteryWidgetProvider());
        return 0;
    }

    public int LockServer(bool fLock) => 0;

    private const int CLASS_E_NOAGGREGATION = -2147221232;
}

/// <summary>
/// Standard COM IClassFactory interface.
/// </summary>
[ComImport, ComVisible(false)]
[InterfaceType(ComInterfaceType.InterfaceIsIUnknown)]
[Guid("00000001-0000-0000-C000-000000000046")]
internal interface IClassFactory
{
    [PreserveSig]
    int CreateInstance(IntPtr pUnkOuter, ref Guid riid, out IntPtr ppvObject);

    [PreserveSig]
    int LockServer(bool fLock);
}
