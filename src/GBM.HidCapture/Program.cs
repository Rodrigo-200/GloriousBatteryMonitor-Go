using System.Collections.Concurrent;
using System.Diagnostics;
using System.Text;
using System.Threading;
using HidSharp;

namespace GBM.HidCapture;

internal static class Program
{
    private const int GloriousVendorId = 0x258A;
    private const int AlternateVendorId = 0x093A;

    private static readonly ConcurrentQueue<string> Log = new();
    private static readonly ConcurrentDictionary<string, CancellationTokenSource> ActiveCaptures = new();
    private static readonly object ConsoleLock = new();
    private static bool ModelI2Only;

    private static void Main(string[] args)
    {
        ModelI2Only = args.Contains("--model-i2", StringComparer.OrdinalIgnoreCase);
        var cts = new CancellationTokenSource();

        var savePath = Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.Desktop),
            "gbm_capture.txt");

        Console.WriteLine("=== GBM HID Capture Tool ===");
        Console.WriteLine(ModelI2Only
            ? "Capturing Model I2 interfaces only (0x093A:0x821D/0x821A)"
            : "Capturing from ALL Glorious interfaces (VID 0x093A and 0x258A)");
        Console.WriteLine("Close Glorious Battery Monitor and Glorious CORE before continuing.");
        Console.WriteLine("Leave the dongle plugged in, unplug the mouse cable, and keep moving the mouse.");
        Console.WriteLine();
        Console.WriteLine($"Saving to: {savePath}");
        Console.WriteLine("Press ENTER when the mouse is working wirelessly.");
        Console.ReadLine();
        LogPhase("A: dongle only (wireless)");
        Console.WriteLine();

        // Initial enumeration
        var initialDevices = EnumerateGloriousDevices();
        foreach (var dev in initialDevices)
        {
            LogFound(dev);
            StartCapture(dev, cts.Token);
        }

        // Hotplug detection thread
        var hotplugThread = new Thread(() => HotplugLoop(cts.Token))
        {
            IsBackground = true,
            Name = "Hotplug"
        };
        hotplugThread.Start();

        Console.WriteLine("Capturing wireless mode for 60 seconds...");
        Thread.Sleep(TimeSpan.FromSeconds(60));

        Console.WriteLine();
        Console.WriteLine("Plug in the mouse cable but leave the dongle connected, then press ENTER.");
        Console.ReadLine();
        LogPhase("B: dongle and cable connected");
        Console.WriteLine("Capturing wired transition for 60 seconds...");
        Thread.Sleep(TimeSpan.FromSeconds(60));

        Console.WriteLine();
        Console.WriteLine("Unplug the mouse cable, keep the dongle connected, move the mouse, then press ENTER.");
        Console.ReadLine();
        LogPhase("C: cable unplugged, dongle only again");
        Console.WriteLine("Capturing wireless recovery for 60 seconds...");
        Thread.Sleep(TimeSpan.FromSeconds(60));

        cts.Cancel();

        // Give threads a moment to wind down
        Thread.Sleep(500);

        // Save
        var sb = new StringBuilder();
        while (Log.TryDequeue(out var line))
            sb.AppendLine(line);

        File.WriteAllText(savePath, sb.ToString());

        Console.WriteLine();
        Console.WriteLine($"Saved to {savePath}");
        Console.WriteLine("Send this file for analysis.");
    }

    private static void LogPhase(string phase)
    {
        var line = $"[{DateTime.Now:O}] [PHASE] {phase}";
        WriteLineSync(line);
        Log.Enqueue(line);
    }

    private static List<HidDevice> EnumerateGloriousDevices()
    {
        var list = new List<HidDevice>();
        foreach (var device in DeviceList.Local.GetHidDevices())
        {
            bool isModelI2 = device.VendorID == AlternateVendorId &&
                             (device.ProductID == 0x821D || device.ProductID == 0x821A);
            if (ModelI2Only ? isModelI2 : device.VendorID == GloriousVendorId || device.VendorID == AlternateVendorId)
                list.Add(device);
        }
        return list;
    }

    private static void LogFound(HidDevice dev)
    {
        int maxFeature = dev.GetMaxFeatureReportLength();
        int maxInput = dev.GetMaxInputReportLength();
        int maxOutput = dev.GetMaxOutputReportLength();
        string product;
        try { product = dev.GetProductName() ?? "N/A"; } catch { product = "N/A"; }
        var line = $"[FOUND] VID=0x{dev.VendorID:X4} PID=0x{dev.ProductID:X4} " +
                   $"Product=\"{product}\" Release=0x{dev.ReleaseNumberBcd:X4} " +
                   $"MaxFeature={maxFeature} MaxInput={maxInput} MaxOutput={maxOutput} Path={dev.DevicePath}";
        WriteLineSync(line);
        Log.Enqueue(line);
    }

    private static void StartCapture(HidDevice dev, CancellationToken token)
    {
        int maxInput = dev.GetMaxInputReportLength();
        int maxFeature = dev.GetMaxFeatureReportLength();

        // Model I2 battery traffic uses col01 (64-byte feature trigger) and
        // col05 (8-byte input response). Avoid opening Windows-owned pointer/
        // keyboard collections, which only add expected access errors.
        if (ModelI2Only && maxInput != 8 && maxFeature < 64)
            return;

        var path = dev.DevicePath;
        if (ActiveCaptures.ContainsKey(path))
            return;

        var captureCts = CancellationTokenSource.CreateLinkedTokenSource(token);
        if (!ActiveCaptures.TryAdd(path, captureCts))
            return;

        // Only capture interfaces that have input reports (maxInput > 0)
        // Also capture feature-only interfaces via GetFeature polling
        if (maxInput > 0)
        {
            var thread = new Thread(() => InputCaptureLoop(dev, captureCts.Token))
            {
                IsBackground = true,
                Name = $"Cap-{dev.VendorID:X4}:{dev.ProductID:X4}-Input-{path.GetHashCode():X8}"
            };
            thread.Start();
        }

        if (maxFeature > 0)
        {
            var thread = new Thread(() => FeatureCaptureLoop(dev, captureCts.Token))
            {
                IsBackground = true,
                Name = $"Cap-{dev.VendorID:X4}:{dev.ProductID:X4}-Feature-{path.GetHashCode():X8}"
            };
            thread.Start();
        }
    }

    private static void InputCaptureLoop(HidDevice dev, CancellationToken token)
    {
        while (!token.IsCancellationRequested)
        {
            HidStream? stream = null;
            try
            {
                stream = dev.Open();
                stream.ReadTimeout = 5000;

                while (!token.IsCancellationRequested)
                {
                    try
                    {
                        var data = stream.Read();
                        var ts = DateTime.Now.ToString("HH:mm:ss.fff");
                        var hex = BitConverter.ToString(data).Replace("-", " ").ToLowerInvariant();
                        var line = $"[{ts}] VID=0x{dev.VendorID:X4} PID=0x{dev.ProductID:X4} " +
                                   $"Path={dev.DevicePath} | {hex}";
                        WriteLineSync(line);
                        Log.Enqueue(line);
                    }
                    catch (TimeoutException)
                    {
                        var ts = DateTime.Now.ToString("HH:mm:ss.fff");
                        var line = $"[{ts}] VID=0x{dev.VendorID:X4} PID=0x{dev.ProductID:X4} " +
                                   $"Path={dev.DevicePath} | (timeout)";
                        Log.Enqueue(line);
                    }
                }
            }
            catch (Exception ex) when (!token.IsCancellationRequested)
            {
                var ts = DateTime.Now.ToString("HH:mm:ss.fff");
                var line = $"[{ts}] VID=0x{dev.VendorID:X4} PID=0x{dev.ProductID:X4} " +
                           $"Path={dev.DevicePath} | ERROR: {ex.Message}";
                Log.Enqueue(line);

                Thread.Sleep(1000);
            }
            finally
            {
                try { stream?.Dispose(); } catch { }
            }
        }
    }

    private static void FeatureCaptureLoop(HidDevice dev, CancellationToken token)
    {
        // Candidate F report IDs, in the same order used by the app.
        byte[] reportIds = { 0x03, 0x00, 0x01, 0x02 };

        while (!token.IsCancellationRequested)
        {
            try
            {
                foreach (var rid in reportIds)
                {
                    if (token.IsCancellationRequested) break;

                    var elapsed = Stopwatch.StartNew();
                    try
                    {
                        using var stream = dev.Open();
                        stream.ReadTimeout = 2000;
                        var buf = new byte[dev.GetMaxFeatureReportLength()];
                        buf[0] = rid;
                        stream.GetFeature(buf);

                        var ts = DateTime.Now.ToString("HH:mm:ss.fff");
                        var hex = BitConverter.ToString(buf).Replace("-", " ").ToLowerInvariant();
                        var line = $"[{ts}] VID=0x{dev.VendorID:X4} PID=0x{dev.ProductID:X4} " +
                                   $"Path={dev.DevicePath} | GetFeature(RID=0x{rid:X2}, {elapsed.ElapsedMilliseconds}ms): {hex}";
                        WriteLineSync(line);
                        Log.Enqueue(line);
                    }
                    catch (Exception ex)
                    {
                        var ts = DateTime.Now.ToString("HH:mm:ss.fff");
                        var line = $"[{ts}] VID=0x{dev.VendorID:X4} PID=0x{dev.ProductID:X4} " +
                                   $"Path={dev.DevicePath} | GetFeature(RID=0x{rid:X2}, {elapsed.ElapsedMilliseconds}ms): ERROR {ex.Message}";
                        Log.Enqueue(line);
                    }
                }
            }
            catch (Exception ex) when (!token.IsCancellationRequested)
            {
                var ts = DateTime.Now.ToString("HH:mm:ss.fff");
                var line = $"[{ts}] VID=0x{dev.VendorID:X4} PID=0x{dev.ProductID:X4} " +
                           $"Path={dev.DevicePath} | FeatureLoop ERROR: {ex.Message}";
                Log.Enqueue(line);
            }

            // Wait 5 seconds before next poll round
            for (int i = 0; i < 50 && !token.IsCancellationRequested; i++)
                Thread.Sleep(100);
        }
    }

    private static void HotplugLoop(CancellationToken token)
    {
        var knownPaths = new HashSet<string>();

        // Seed with initially known devices
        foreach (var dev in EnumerateGloriousDevices())
            knownPaths.Add(dev.DevicePath);

        while (!token.IsCancellationRequested)
        {
            // Wait 5 seconds (in small increments so we can cancel promptly)
            for (int i = 0; i < 50 && !token.IsCancellationRequested; i++)
                Thread.Sleep(100);

            if (token.IsCancellationRequested) break;

            var currentDevices = EnumerateGloriousDevices();
            var currentPaths = new HashSet<string>(currentDevices.Select(d => d.DevicePath));

            // Detect newly appeared devices
            foreach (var dev in currentDevices)
            {
                if (!knownPaths.Contains(dev.DevicePath))
                {
                    var ts = DateTime.Now.ToString("HH:mm:ss.fff");
                    var line = $"[{ts}] [APPEARED] VID=0x{dev.VendorID:X4} PID=0x{dev.ProductID:X4} " +
                               $"Path={dev.DevicePath}";
                    WriteLineSync(line);
                    Log.Enqueue(line);

                    LogFound(dev);
                    StartCapture(dev, token);
                }
            }

            // Detect disappeared devices
            foreach (var path in knownPaths)
            {
                if (!currentPaths.Contains(path))
                {
                    var ts = DateTime.Now.ToString("HH:mm:ss.fff");
                    var line = $"[{ts}] [DISAPPEARED] Path={path}";
                    WriteLineSync(line);
                    Log.Enqueue(line);

                    // Cancel and remove the capture thread
                    if (ActiveCaptures.TryRemove(path, out var captureCts))
                    {
                        captureCts.Cancel();
                        captureCts.Dispose();
                    }
                }
            }

            knownPaths = currentPaths;
        }
    }

    private static void WriteLineSync(string line)
    {
        lock (ConsoleLock)
        {
            Console.WriteLine(line);
        }
    }
}
