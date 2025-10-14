import { useState, useEffect } from "react";
import { ArrowRight, Download, Github, MousePointer2, Battery, Bolt, Shield, Zap, Stars, MonitorSmartphone, Radio, CheckCircle2, HelpCircle } from "lucide-react";

export default function LandingPage() {
  const [openFaq, setOpenFaq] = useState<number | null>(0);

  const faqs = [
    {
      q: "What is Glorious Battery Monitor?",
      a: "A lightweight Windows companion that shows real-time battery percentage for Glorious mice and lets you set custom low and critical alert thresholds.",
    },
    {
      q: "Which devices are supported?",
      a: "Explicitly tested: Glorious Model D Wireless via its 2.4 GHz dongle. Other Glorious wireless models may work but have not been tested.",
    },
    {
      q: "What platforms are available?",
      a: "Windows only right now. Other OSes could be considered in the future, but there’s no timeline or guarantee.",
    },
    {
      q: "Is it open-source?",
      a: "Yes. You can review the code, suggest features, or contribute fixes on GitHub.",
    },
  ];

  // GitHub repo + dynamic release data
  const REPO_OWNER = "Rodrigo-200";
  const REPO_NAME = "GloriousBatteryMonitor";
  const repoUrl = `https://github.com/${REPO_OWNER}/${REPO_NAME}`;
  const releasesUrl = `${repoUrl}/releases`;

  const [version, setVersion] = useState<string>("");
  const [exeUrl, setExeUrl] = useState<string>("");
  const [portableZipUrl, setPortableZipUrl] = useState<string>("");
  const [sourceZipUrl, setSourceZipUrl] = useState<string>("");

  useEffect(() => {
    const load = async () => {
      try {
        const res = await fetch(`https://api.github.com/repos/${REPO_OWNER}/${REPO_NAME}/releases/latest`);
        if (!res.ok) throw new Error("Failed to fetch latest release");
        const data = await res.json();
        setVersion(data.tag_name || data.name || "");

        if (Array.isArray(data.assets)) {
          let exe = "";
          let zip = "";
          for (const a of data.assets) {
            const name = String(a.name || "").toLowerCase();
            if (!exe && name.endsWith(".exe")) exe = a.browser_download_url;
            if (!zip && name.endsWith(".zip")) zip = a.browser_download_url;
          }
          if (exe) setExeUrl(exe);
          if (zip) setPortableZipUrl(zip);
        }
        if (data.zipball_url) setSourceZipUrl(data.zipball_url);
        if (!portableZipUrl && data.zipball_url) setPortableZipUrl(data.zipball_url);
      } catch (e) {
        console.error(e);
      }
    };
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <div className="min-h-screen bg-neutral-950 text-neutral-100 selection:bg-amber-300/30 selection:text-amber-900">
      {/* NAVBAR */}
      <header className="sticky top-0 z-30 backdrop-blur supports-[backdrop-filter]:bg-neutral-950/60 border-b border-neutral-800/60">
        <div className="mx-auto max-w-6xl px-4 py-3 flex items-center justify-between">
          <a href="#home" className="flex items-center gap-2">
            <div className="h-8 w-8 rounded-xl bg-gradient-to-br from-amber-400 via-amber-500 to-yellow-400 shadow-[0_0_40px_-12px_rgba(251,191,36,0.7)] grid place-items-center">
              <MousePointer2 className="h-4 w-4 text-neutral-950"/>
            </div>
            <span className="font-semibold tracking-tight">Glorious Battery Monitor</span>
          </a>
          <nav className="hidden md:flex items-center gap-7 text-sm text-neutral-300">
            <a href="#features" className="hover:text-white">Features</a>
            <a href="#how" className="hover:text-white">How it works</a>
            <a href="#download" className="hover:text-white">Download</a>
            <a href="#faq" className="hover:text-white">FAQ</a>
          </nav>
          <div className="flex items-center gap-3">
            <a href={exeUrl || releasesUrl} target="_blank" rel="noopener noreferrer" className="hidden sm:inline-flex items-center gap-2 rounded-xl bg-amber-400 px-4 py-2 font-medium text-neutral-900 shadow hover:shadow-lg transition">
              <Download className="h-4 w-4"/>
              <span>Get the App</span>
            </a>
            <a href={repoUrl} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-2 rounded-xl border border-neutral-800 px-3 py-2 text-sm hover:bg-neutral-900">
              <Github className="h-4 w-4"/>
              <span>GitHub</span>
            </a>
          </div>
        </div>
      </header>

      {/* HERO */}
      <section id="home" className="relative overflow-hidden">
        <div className="pointer-events-none absolute inset-0 bg-gradient-to-b from-amber-500/5 via-transparent to-transparent" />
        <div className="mx-auto max-w-6xl px-4 py-24 sm:py-28">
          <div className="grid lg:grid-cols-2 gap-10 items-center">
            <div>
              <p className="inline-block rounded-full border border-amber-400/30 bg-amber-400/10 px-3 py-1 text-xs text-amber-300 mb-4">Real‑time battery for Glorious mice</p>
              <h1 className="text-4xl sm:text-5xl lg:text-6xl font-semibold tracking-tight leading-[1.05]">
                Never get caught with a <span className="text-amber-400">dead mouse</span> again.
              </h1>
              <p className="mt-5 text-neutral-300 text-lg max-w-xl">
                Glorious Battery Monitor shows precise battery percentage and charge state in your Windows system tray — with customizable low and critical alerts.
              </p>
              <div className="mt-8 flex flex-wrap items-center gap-3">
                <a href={exeUrl || releasesUrl} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-2 rounded-2xl bg-amber-400 px-5 py-3 font-medium text-neutral-900 shadow hover:shadow-lg transition group">
                  <Download className="h-5 w-5"/>
                  <span>Download for Windows</span>
                  <ArrowRight className="h-5 w-5 translate-x-0 group-hover:translate-x-0.5 transition"/>
                </a>
                <a href={repoUrl} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-2 rounded-2xl border border-neutral-800 px-5 py-3 font-medium hover:bg-neutral-900">
                  <Github className="h-5 w-5"/>
                  <span>View on GitHub</span>
                </a>
              </div>
              <div className="mt-6 flex items-center gap-4 text-sm text-neutral-400">
                <div className="flex items-center gap-2"><Shield className="h-4 w-4"/> Open-source</div>
                <div className="flex items-center gap-2"><Bolt className="h-4 w-4"/> Lightweight</div>
                <div className="flex items-center gap-2"><Stars className="h-4 w-4"/> Gamer‑friendly</div>
              </div>
            </div>
            <div className="relative">
              <div className="absolute -inset-6 rounded-[2rem] bg-amber-400/10 blur-2xl"/>
              <div className="relative rounded-[2rem] border border-neutral-800 bg-gradient-to-b from-neutral-900 to-neutral-950 p-6 shadow-2xl">
                <div className="aspect-[16/10] rounded-xl border border-neutral-800 bg-neutral-950 grid place-items-center">
                  <MonitorSmartphone className="h-10 w-10 text-amber-300"/>
                </div>
                <div className="mt-4 grid grid-cols-3 gap-3 text-xs text-neutral-300">
                  <div className="flex items-center gap-2 rounded-lg border border-neutral-800 p-2"><Battery className="h-4 w-4 text-amber-300"/> 87%</div>
                  <div className="flex items-center gap-2 rounded-lg border border-neutral-800 p-2"><Radio className="h-4 w-4"/> 2.4 GHz</div>
                  <div className="flex items-center gap-2 rounded-lg border border-neutral-800 p-2"><Zap className="h-4 w-4"/> Charging</div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </section>

      {/* FEATURES */}
      <section id="features" className="mx-auto max-w-6xl px-4 py-20">
        <div className="text-center mb-12">
          <h2 className="text-3xl sm:text-4xl font-semibold tracking-tight">Everything you need at a glance</h2>
          <p className="mt-3 text-neutral-300">Designed for focus. Built for speed.</p>
        </div>
        <div className="grid md:grid-cols-2 lg:grid-cols-3 gap-6">
          {[
            { icon: Battery, title: "Live battery %", desc: "Reads precise percentage from your mouse over HID and updates in real time." },
            { icon: Zap, title: "Custom alerts", desc: "Set your own low and critical thresholds for timely warnings." },
            { icon: Bolt, title: "Ultra‑light", desc: "Tiny footprint and RAM usage. No bloat, no RGB control nonsense." },
            { icon: Shield, title: "Safe & open", desc: "No telemetry. Open-source so you can audit every line." },
            { icon: MonitorSmartphone, title: "Windows tray icon", desc: "Clean system tray indicator with status at a glance." },
            { icon: MousePointer2, title: "Made for Glorious mice", desc: "Designed for Glorious wireless mice using their 2.4 GHz dongles." },
          ].map((f, i) => (
            <div key={i} className="rounded-2xl border border-neutral-800 bg-neutral-950 p-5 hover:border-amber-500/40 transition">
              <f.icon className="h-6 w-6 text-amber-300"/>
              <h3 className="mt-3 font-semibold">{f.title}</h3>
              <p className="mt-2 text-sm text-neutral-300">{f.desc}</p>
            </div>
          ))}
        </div>
      </section>

      {/* HOW IT WORKS */}
      <section id="how" className="mx-auto max-w-6xl px-4 py-20 border-y border-neutral-800/60 bg-gradient-to-b from-neutral-950 to-neutral-950">
        <div className="grid lg:grid-cols-3 gap-8">
          {[
            {
              step: 1,
              title: "Plug in your dongle",
              desc: "Connect the Glorious 2.4 GHz receiver for your mouse.",
            },
            {
              step: 2,
              title: "Launch the app",
              desc: "The monitor auto‑detects your device and starts reading HID reports.",
            },
            {
              step: 3,
              title: "Play without worry",
              desc: "A Windows tray indicator keeps you informed with zero distraction.",
            },
          ].map((s, i) => (
            <div key={i} className="relative rounded-2xl border border-neutral-800 bg-neutral-950 p-6">
              <div className="absolute -top-3 -left-3 rounded-xl bg-amber-400 px-3 py-1 text-neutral-900 text-xs font-semibold shadow">Step {s.step}</div>
              <h3 className="mt-3 text-lg font-semibold">{s.title}</h3>
              <p className="mt-2 text-sm text-neutral-300">{s.desc}</p>
            </div>
          ))}
        </div>
      </section>

      {/* DOWNLOAD */}
      <section id="download" className="mx-auto max-w-6xl px-4 py-20">
        <div className="rounded-3xl border border-neutral-800 bg-gradient-to-br from-neutral-900 to-neutral-950 p-8 md:p-12 text-center">
          <h2 className="text-3xl sm:text-4xl font-semibold tracking-tight">Download and start monitoring</h2>
          <p className="mt-3 text-neutral-300">Windows build with auto‑updates. Portable ZIP also available.</p>

          <div className="mt-3 text-sm text-neutral-400">
            {version ? (
              <span>Current version: <span className="text-neutral-200 font-medium">{version}</span></span>
            ) : (
              <span>Fetching latest version…</span>
            )}
          </div>

          <div className="mt-6 flex flex-wrap gap-3 justify-center">
            <a href={exeUrl || releasesUrl} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-2 rounded-2xl bg-amber-400 px-5 py-3 font-medium text-neutral-900 shadow hover:shadow-lg">
              <Download className="h-5 w-5"/>
              <span>Download for Windows (.exe)</span>
            </a>
            <a href={portableZipUrl || releasesUrl} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-2 rounded-2xl border border-neutral-800 px-5 py-3 font-medium hover:bg-neutral-900">
              <Download className="h-5 w-5"/>
              <span>Portable ZIP</span>
            </a>
            <a href={sourceZipUrl || releasesUrl} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-2 rounded-2xl border border-neutral-800 px-5 py-3 font-medium hover:bg-neutral-900">
              <Github className="h-5 w-5"/>
              <span>Source code (.zip)</span>
            </a>
            <a href={releasesUrl} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-2 rounded-2xl border border-neutral-800 px-5 py-3 font-medium hover:bg-neutral-900">
              <Download className="h-5 w-5"/>
              <span>All releases</span>
            </a>
          </div>
          <div className="mt-4 text-xs text-neutral-400">Checksums & release notes on <a href={releasesUrl} target="_blank" rel="noopener noreferrer" className="underline hover:text-amber-300">GitHub</a>.</div>
        </div>
      </section>

      {/* FAQ */}
      <section id="faq" className="mx-auto max-w-3xl px-4 py-16">
        <h2 className="text-2xl sm:text-3xl font-semibold tracking-tight text-center">FAQ</h2>
        <div className="mt-6 divide-y divide-neutral-800 rounded-2xl border border-neutral-800 bg-neutral-950">
          {faqs.map((f, i) => (
            <button
              key={i}
              onClick={() => setOpenFaq(openFaq === i ? null : i)}
              className="w-full text-left p-5 focus:outline-none"
            >
              <div className="flex items-start justify-between gap-4">
                <div>
                  <div className="flex items-center gap-2">
                    <HelpCircle className="h-4 w-4 text-amber-300"/>
                    <span className="font-medium">{f.q}</span>
                  </div>
                  {openFaq === i && (
                    <p className="mt-2 text-sm text-neutral-300">{f.a}</p>
                  )}
                </div>
                <CheckCircle2 className={`h-5 w-5 transition ${openFaq === i ? "opacity-100" : "opacity-0"}`}/>
              </div>
            </button>
          ))}
        </div>
      </section>

      {/* FOOTER */}
      <footer className="border-t border-neutral-800/60 py-10">
        <div className="mx-auto max-w-6xl px-4 flex flex-col sm:flex-row items-center justify-between gap-4 text-sm text-neutral-400">
          <div>© {new Date().getFullYear()} Glorious Battery Monitor — Community project</div>
          <div className="flex items-center gap-5">
            <a href={repoUrl} target="_blank" rel="noopener noreferrer" className="hover:text-neutral-200">GitHub</a>
            <a href="#privacy" className="hover:text-neutral-200">Privacy</a>
            <a href="#contact" className="hover:text-neutral-200">Contact</a>
          </div>
        </div>
      </footer>
    </div>
  );
}
