using System.Collections.Concurrent;
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

    private static void Main()
    {
        var cts = new CancellationTokenSource();

        var savePath = Path.Combine(
            Environment.GetFolderPath(Environment.SpecialFolder.Desktop),
            "gbm_capture.txt");

        Console.WriteLine("=== GBM HID Capture Tool ===");
        Console.WriteLine("Capturing from ALL Glorious interfaces (VID 0x093A and 0x258A)");
        Console.WriteLine("Plug in your mouse cable AND leave the dongle plugged in.");
        Console.WriteLine("Move the mouse around during capture.");
        Console.WriteLine("Press ENTER to stop and save.");
        Console.WriteLine();
        Console.WriteLine($"Saving to: {savePath}");
        Console.WriteLine("Starting capture...");
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

        Console.WriteLine();

        // Wait for ENTER
        Console.ReadLine();
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

    private static List<HidDevice> EnumerateGloriousDevices()
    {
        var list = new List<HidDevice>();
        foreach (var device in DeviceList.Local.GetHidDevices())
        {
            if (device.VendorID == GloriousVendorId || device.VendorID == AlternateVendorId)
                list.Add(device);
        }
        return list;
    }

    private static void LogFound(HidDevice dev)
    {
        int maxFeature = dev.GetMaxFeatureReportLength();
        int maxInput = dev.GetMaxInputReportLength();
        var line = $"[FOUND] VID=0x{dev.VendorID:X4} PID=0x{dev.ProductID:X4} " +
                   $"MaxFeature={maxFeature} MaxInput={maxInput} Path={dev.DevicePath}";
        WriteLineSync(line);
        Log.Enqueue(line);
    }

    private static void StartCapture(HidDevice dev, CancellationToken token)
    {
        var path = dev.DevicePath;
        if (ActiveCaptures.ContainsKey(path))
            return;

        var captureCts = CancellationTokenSource.CreateLinkedTokenSource(token);
        if (!ActiveCaptures.TryAdd(path, captureCts))
            return;

        int maxInput = dev.GetMaxInputReportLength();
        int maxFeature = dev.GetMaxFeatureReportLength();

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
                        WriteLineSync(line);
                        Log.Enqueue(line);
                    }
                }
            }
            catch (Exception ex) when (!token.IsCancellationRequested)
            {
                var ts = DateTime.Now.ToString("HH:mm:ss.fff");
                var line = $"[{ts}] VID=0x{dev.VendorID:X4} PID=0x{dev.ProductID:X4} " +
                           $"Path={dev.DevicePath} | ERROR: {ex.Message}";
                WriteLineSync(line);
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
        // Poll GetFeature with common report IDs every 5 seconds
        byte[] reportIds = { 0x00, 0x03, 0x04, 0x05, 0x06 };

        while (!token.IsCancellationRequested)
        {
            HidStream? stream = null;
            try
            {
                stream = dev.Open();
                stream.ReadTimeout = 2000;

                foreach (var rid in reportIds)
                {
                    if (token.IsCancellationRequested) break;

                    try
                    {
                        var buf = new byte[dev.GetMaxFeatureReportLength()];
                        buf[0] = rid;
                        stream.GetFeature(buf);

                        var ts = DateTime.Now.ToString("HH:mm:ss.fff");
                        var hex = BitConverter.ToString(buf).Replace("-", " ").ToLowerInvariant();
                        var line = $"[{ts}] VID=0x{dev.VendorID:X4} PID=0x{dev.ProductID:X4} " +
                                   $"Path={dev.DevicePath} | GetFeature(RID=0x{rid:X2}): {hex}";
                        WriteLineSync(line);
                        Log.Enqueue(line);
                    }
                    catch (Exception ex)
                    {
                        var ts = DateTime.Now.ToString("HH:mm:ss.fff");
                        var line = $"[{ts}] VID=0x{dev.VendorID:X4} PID=0x{dev.ProductID:X4} " +
                                   $"Path={dev.DevicePath} | GetFeature(RID=0x{rid:X2}): ERROR {ex.Message}";
                        WriteLineSync(line);
                        Log.Enqueue(line);
                    }
                }
            }
            catch (Exception ex) when (!token.IsCancellationRequested)
            {
                var ts = DateTime.Now.ToString("HH:mm:ss.fff");
                var line = $"[{ts}] VID=0x{dev.VendorID:X4} PID=0x{dev.ProductID:X4} " +
                           $"Path={dev.DevicePath} | FeatureLoop ERROR: {ex.Message}";
                WriteLineSync(line);
                Log.Enqueue(line);
            }
            finally
            {
                try { stream?.Dispose(); } catch { }
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
