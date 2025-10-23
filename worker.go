package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/sstallion/go-hid"
)

const (
	// Longer timeouts here give the helper process enough time to
	// initialize and probe slow or flaky dongles without spurious
	// "timeout" failures.
	// Increase timeouts so slow or flaky wireless dongles have more
	// time to respond. Longer timeouts make probe/adoption slower but
	// much more reliable for problematic receivers.
	// Probe timeout bounds a full scan through report IDs in the worker.
	workerProbeTimeout = 8000 * time.Millisecond
	// Per-operation timeout used by the parent when waiting for worker RPCs.
	workerOpTimeout = 10000 * time.Millisecond
	// Timeout used inside the worker to bound potentially-blocking
	// GetFeatureReport calls on session/worker opens. This prevents a
	// hung driver from blocking the helper indefinitely and ensures the
	// parent process receives a timely 'timeout' response.
	// Give the helper more time for GetFeatureReport on slow receivers.
	workerFeatureTimeout = 6000 * time.Millisecond
)

// WorkerClient manages a helper process that performs temporary handle
// probing and pokes so the main process won't be killed if a driver
// misbehaves during those operations.
type WorkerClient struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	enc     *json.Encoder
	dec     *json.Decoder
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	running bool
	// async request/response support
	pending   map[int]chan map[string]interface{}
	pendingMu sync.Mutex
	nextID    int
	// events and input frames coming from worker
	events  chan map[string]interface{}
	frames  chan []byte
	writeMu sync.Mutex
}

var probeWorker *WorkerClient
var probeWorkerMu sync.Mutex
var workerExitMu sync.Mutex
var lastWorkerExit time.Time
var probeWorkerLastStart time.Time

// StartProbeWorker launches the helper process if not running.
func StartProbeWorker() error {
	probeWorkerMu.Lock()
	defer probeWorkerMu.Unlock()
	// If a worker is already present, attempt a restart if it isn't
	// running. This covers the case where the helper process crashed
	// and we still hold a stale WorkerClient instance.
	if probeWorker != nil {
		probeWorker.mu.Lock()
		running := probeWorker.running
		probeWorker.mu.Unlock()
		if running {
			return nil
		}
		// avoid aggressive restart loops
		if time.Since(probeWorkerLastStart) < 1*time.Second {
			return fmt.Errorf("probe worker recently started; backing off")
		}
		if err := probeWorker.start(); err != nil {
			return err
		}
		probeWorkerLastStart = time.Now()
		return nil
	}

	wc := &WorkerClient{}
	if err := wc.start(); err != nil {
		return err
	}
	probeWorker = wc
	probeWorkerLastStart = time.Now()
	return nil
}

func (w *WorkerClient) start() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running {
		return nil
	}
	if logger != nil {
		logger.Printf("[WORKER] starting helper process")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "--hid-worker")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// Capture worker stderr so we can log any worker-side panics or
	// driver-level error traces into our debug.log for diagnosis.
	stderrPipe, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		if logger != nil {
			logger.Printf("[WORKER] failed to start helper: %v", err)
		}
		return err
	}
	// Start a goroutine to read/stash stderr lines from the worker.
	go func() {
		if stderrPipe == nil {
			return
		}
		scanner := bufio.NewScanner(stderrPipe)
		for scanner.Scan() {
			line := scanner.Text()
			if logger != nil {
				logger.Printf("[WORKER:stderr] %s", line)
			}
		}
	}()
	w.cmd = cmd
	w.stdin = stdin
	w.stdout = stdout
	w.enc = json.NewEncoder(stdin)
	// Use buffered reader so decoder won't block oddly on partial writes
	w.dec = json.NewDecoder(bufio.NewReader(stdout))
	w.pending = make(map[int]chan map[string]interface{})
	w.events = make(chan map[string]interface{}, 64)
	w.frames = make(chan []byte, 64)
	w.running = true
	if logger != nil {
		logger.Printf("[WORKER] helper started (pid=%d)", cmd.Process.Pid)
	}
	// monitor worker exit
	go func() {
		err := cmd.Wait()
		w.mu.Lock()
		w.running = false
		// close channels so consumers stop
		close(w.events)
		close(w.frames)
		// notify all pending requests of failure
		w.pendingMu.Lock()
		for id, ch := range w.pending {
			close(ch)
			delete(w.pending, id)
		}
		w.pendingMu.Unlock()
		w.mu.Unlock()

		// Clear the global probeWorker pointer so future StartProbeWorker
		// calls create a fresh helper instance.
		probeWorkerMu.Lock()
		if probeWorker == w {
			probeWorker = nil
		}
		probeWorkerMu.Unlock()

		// Record the time of worker exit so callers can avoid aggressive
		// local pokes immediately after a helper termination which is a
		// common time for driver re-enumeration and instability.
		workerExitMu.Lock()
		lastWorkerExit = time.Now()
		workerExitMu.Unlock()

		if logger != nil {
			logger.Printf("[WORKER] helper exited: %v", err)
		}
	}()
	// Start reading loop handling both responses and spontaneous events
	go w.readLoop()
	return nil
}

func (w *WorkerClient) readLoop() {
	for {
		var msg map[string]interface{}
		if err := w.dec.Decode(&msg); err != nil {
			// fatal — break and let monitor goroutine mark running=false
			return
		}
		// dispatch by presence of id or event key
		if idv, ok := msg["id"]; ok {
			if idf, ok := idv.(float64); ok {
				id := int(idf)
				w.pendingMu.Lock()
				ch, ok := w.pending[id]
				if ok {
					delete(w.pending, id)
				}
				w.pendingMu.Unlock()
				if ok {
					// send to pending reply channel; recover from races
					safeSendPending(ch, msg)
				}
				continue
			}
		}
		if ev, ok := msg["event"].(string); ok {
			if ev == "frame" {
				if fh, ok := msg["frameHex"].(string); ok {
					if b, err := hex.DecodeString(fh); err == nil {
						w.safeSendFrame(b)
						continue
					}
				}
			}
			// other events go to events chan
			w.safeSendEvent(msg)
			continue
		}
	}
}

// safeSendPending attempts to send a reply to the provided pending
// request channel. It recovers from panics which can occur if the
// monitor goroutine closed the channel while we attempted to deliver
// the reply.
func safeSendPending(ch chan map[string]interface{}, msg map[string]interface{}) {
	defer func() {
		if r := recover(); r != nil {
			// best-effort: if the pending channel is gone, the caller
			// will time out or be notified by the monitor closure.
		}
	}()
	ch <- msg
}

// safeSendEvent writes to the events channel but recovers if the
// channel has been closed concurrently by the monitor.
func (w *WorkerClient) safeSendEvent(ev map[string]interface{}) {
	defer func() {
		if r := recover(); r != nil {
		}
	}()
	select {
	case w.events <- ev:
	default:
	}
}

// safeSendFrame writes an input frame to the frames channel non-blocking
// and recovers if the channel has been closed.
func (w *WorkerClient) safeSendFrame(b []byte) {
	defer func() {
		if r := recover(); r != nil {
		}
	}()
	select {
	case w.frames <- b:
	default:
	}
}

// ProbePath asks the worker to open the given HID path and attempt
// feature report reads of different sizes. Returns parsed battery info.
func (w *WorkerClient) ProbePath(path string, reportID byte) (int, bool, bool, int, error) {
	w.mu.Lock()
	if !w.running {
		if err := w.start(); err != nil {
			w.mu.Unlock()
			return 0, false, false, 0, err
		}
	}
	id := w.nextID
	w.nextID++
	ch := make(chan map[string]interface{}, 1)
	w.pendingMu.Lock()
	w.pending[id] = ch
	w.pendingMu.Unlock()
	w.mu.Unlock()
	req := map[string]interface{}{"id": id, "cmd": "probe", "path": path, "reportId": int(reportID)}
	w.writeMu.Lock()
	if err := w.enc.Encode(req); err != nil {
		w.writeMu.Unlock()
		return 0, false, false, 0, err
	}
	w.writeMu.Unlock()
	select {
	case resp, ok := <-ch:
		if !ok {
			return 0, false, false, 0, fmt.Errorf("worker failed")
		}
		if okv, _ := resp["ok"].(bool); !okv {
			if e, _ := resp["error"].(string); e != "" {
				return 0, false, false, 0, fmt.Errorf(e)
			}
			return 0, false, false, 0, fmt.Errorf("probe failed")
		}
		lvl := int(resp["level"].(float64))
		chg := resp["charging"].(bool)
		usedLen := int(resp["len"].(float64))
		return lvl, chg, true, usedLen, nil
	case <-time.After(workerProbeTimeout):
		// remove pending so worker's eventual reply is discarded
		w.pendingMu.Lock()
		delete(w.pending, id)
		w.pendingMu.Unlock()
		return 0, false, false, 0, fmt.Errorf("worker probe timeout")
	}
}

// ProbePathAll asks the worker to scan all configured probe RIDs in one
// operation and return the first usable report. This reduces RPC
// round-trips compared to calling ProbePath repeatedly for each RID.
func (w *WorkerClient) ProbePathAll(path string) (int, bool, bool, byte, int, error) {
	w.mu.Lock()
	if !w.running {
		if err := w.start(); err != nil {
			w.mu.Unlock()
			return 0, false, false, 0, 0, err
		}
	}
	id := w.nextID
	w.nextID++
	ch := make(chan map[string]interface{}, 1)
	w.pendingMu.Lock()
	w.pending[id] = ch
	w.pendingMu.Unlock()
	w.mu.Unlock()
	req := map[string]interface{}{"id": id, "cmd": "probe_all", "path": path}
	w.writeMu.Lock()
	if err := w.enc.Encode(req); err != nil {
		w.writeMu.Unlock()
		return 0, false, false, 0, 0, err
	}
	w.writeMu.Unlock()

	select {
	case resp, ok := <-ch:
		if !ok {
			return 0, false, false, 0, 0, fmt.Errorf("worker failed")
		}
		if okv, _ := resp["ok"].(bool); !okv {
			if e, _ := resp["error"].(string); e != "" {
				return 0, false, false, 0, 0, fmt.Errorf(e)
			}
			return 0, false, false, 0, 0, fmt.Errorf("probe failed")
		}
		lvl := int(resp["level"].(float64))
		chg := resp["charging"].(bool)
		rid := byte(int(resp["rid"].(float64)))
		usedLen := int(resp["len"].(float64))
		return lvl, chg, true, rid, usedLen, nil
	case <-time.After(workerProbeTimeout * 2):
		w.pendingMu.Lock()
		delete(w.pending, id)
		w.pendingMu.Unlock()
		return 0, false, false, 0, 0, fmt.Errorf("worker probe_all timeout")
	}
}

// PokePath asks the worker to open the path and send the provided body as
// a feature/write probe. The body is provided as a hex string by the
// caller.
func (w *WorkerClient) PokePath(path string, reportID byte, bodyHex string) error {
	w.mu.Lock()
	if !w.running {
		if err := w.start(); err != nil {
			w.mu.Unlock()
			return err
		}
	}
	id := w.nextID
	w.nextID++
	ch := make(chan map[string]interface{}, 1)
	w.pendingMu.Lock()
	w.pending[id] = ch
	w.pendingMu.Unlock()
	w.mu.Unlock()
	req := map[string]interface{}{"id": id, "cmd": "poke", "path": path, "reportId": int(reportID), "bodyHex": bodyHex}
	w.writeMu.Lock()
	if err := w.enc.Encode(req); err != nil {
		w.writeMu.Unlock()
		return err
	}
	w.writeMu.Unlock()
	select {
	case resp, ok := <-ch:
		if !ok {
			return fmt.Errorf("worker failed")
		}
		if okv, _ := resp["ok"].(bool); !okv {
			if e, _ := resp["error"].(string); e != "" {
				return fmt.Errorf(e)
			}
			return fmt.Errorf("poke failed")
		}
		return nil
	case <-time.After(workerOpTimeout):
		w.pendingMu.Lock()
		delete(w.pending, id)
		w.pendingMu.Unlock()
		return fmt.Errorf("worker poke timeout")
	}
}

// StartSession asks the worker to open and hold a persistent handle for
// the provided path and to start streaming input reports back to us.
func (w *WorkerClient) StartSession(path string) error {
	w.mu.Lock()
	if !w.running {
		if err := w.start(); err != nil {
			w.mu.Unlock()
			return err
		}
	}
	id := w.nextID
	w.nextID++
	ch := make(chan map[string]interface{}, 1)
	w.pendingMu.Lock()
	w.pending[id] = ch
	w.pendingMu.Unlock()
	w.mu.Unlock()
	req := map[string]interface{}{"id": id, "cmd": "open_session", "path": path}
	w.writeMu.Lock()
	if err := w.enc.Encode(req); err != nil {
		w.writeMu.Unlock()
		return err
	}
	w.writeMu.Unlock()
	// Wait only briefly for an acknowledgment from the worker that it
	// accepted the request. The actual device open happens inside the
	// worker in the background and will emit a session_opened or
	// session_error event which callers can observe via the events
	// channel if they need confirmation. A short ack avoids long
	// blocking when the worker is slow to open the device.
	ackTimeout := 800 * time.Millisecond
	select {
	case resp, ok := <-ch:
		if !ok {
			return fmt.Errorf("worker failed")
		}
		if okv, _ := resp["ok"].(bool); !okv {
			if e, _ := resp["error"].(string); e != "" {
				return fmt.Errorf(e)
			}
			return fmt.Errorf("open_session failed")
		}
		return nil
	case <-time.After(ackTimeout):
		// If the worker didn't ack quickly, give up so callers can
		// proceed and rely on the events/frames path instead of
		// blocking the main thread indefinitely.
		w.pendingMu.Lock()
		delete(w.pending, id)
		w.pendingMu.Unlock()
		return fmt.Errorf("open_session ack timeout")
	}
}

// CloseSession asks the worker to close an existing persistent handle.
func (w *WorkerClient) CloseSession() error {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return fmt.Errorf("worker not running")
	}
	id := w.nextID
	w.nextID++
	ch := make(chan map[string]interface{}, 1)
	w.pendingMu.Lock()
	w.pending[id] = ch
	w.pendingMu.Unlock()
	w.mu.Unlock()
	req := map[string]interface{}{"id": id, "cmd": "close_session"}
	w.writeMu.Lock()
	if err := w.enc.Encode(req); err != nil {
		w.writeMu.Unlock()
		return err
	}
	w.writeMu.Unlock()
	select {
	case resp, ok := <-ch:
		if !ok {
			return fmt.Errorf("worker failed")
		}
		if okv, _ := resp["ok"].(bool); !okv {
			if e, _ := resp["error"].(string); e != "" {
				return fmt.Errorf(e)
			}
			return fmt.Errorf("close_session failed")
		}
		return nil
	case <-time.After(workerOpTimeout):
		w.pendingMu.Lock()
		delete(w.pending, id)
		w.pendingMu.Unlock()
		return fmt.Errorf("close_session timeout")
	}
}

// InputChannel returns a channel of input report frames provided by the worker.
func (w *WorkerClient) InputChannel() <-chan []byte { return w.frames }

// GetFeatureFromSession attempts to read feature reports using the
// already-open persistent session in the worker. Returns parsed battery info.
func (w *WorkerClient) GetFeatureFromSession(reportID byte) (int, bool, bool, int, error) {
	w.mu.Lock()
	if !w.running {
		if err := w.start(); err != nil {
			w.mu.Unlock()
			return 0, false, false, 0, err
		}
	}
	id := w.nextID
	w.nextID++
	ch := make(chan map[string]interface{}, 1)
	w.pendingMu.Lock()
	w.pending[id] = ch
	w.pendingMu.Unlock()
	w.mu.Unlock()
	req := map[string]interface{}{"id": id, "cmd": "get_feature_session", "reportId": int(reportID)}
	w.writeMu.Lock()
	if err := w.enc.Encode(req); err != nil {
		w.writeMu.Unlock()
		return 0, false, false, 0, err
	}
	w.writeMu.Unlock()
	select {
	case resp, ok := <-ch:
		if !ok {
			return 0, false, false, 0, fmt.Errorf("worker failed")
		}
		if okv, _ := resp["ok"].(bool); !okv {
			if e, _ := resp["error"].(string); e != "" {
				return 0, false, false, 0, fmt.Errorf(e)
			}
			return 0, false, false, 0, fmt.Errorf("no report")
		}
		lvl := int(resp["level"].(float64))
		chg := resp["charging"].(bool)
		usedLen := int(resp["len"].(float64))
		return lvl, chg, true, usedLen, nil
	case <-time.After(workerOpTimeout):
		w.pendingMu.Lock()
		delete(w.pending, id)
		w.pendingMu.Unlock()
		return 0, false, false, 0, fmt.Errorf("get_feature_session timeout")
	}
}

// SendFeatureToSession writes a feature/write to the worker's open session.
func (w *WorkerClient) SendFeatureToSession(reportID byte, bodyHex string) error {
	w.mu.Lock()
	if !w.running {
		if err := w.start(); err != nil {
			w.mu.Unlock()
			return err
		}
	}
	id := w.nextID
	w.nextID++
	ch := make(chan map[string]interface{}, 1)
	w.pendingMu.Lock()
	w.pending[id] = ch
	w.pendingMu.Unlock()
	w.mu.Unlock()
	req := map[string]interface{}{"id": id, "cmd": "send_feature_session", "reportId": int(reportID), "bodyHex": bodyHex}
	w.writeMu.Lock()
	if err := w.enc.Encode(req); err != nil {
		w.writeMu.Unlock()
		return err
	}
	w.writeMu.Unlock()
	select {
	case resp, ok := <-ch:
		if !ok {
			return fmt.Errorf("worker failed")
		}
		if okv, _ := resp["ok"].(bool); !okv {
			if e, _ := resp["error"].(string); e != "" {
				return fmt.Errorf(e)
			}
			return fmt.Errorf("send failed")
		}
		return nil
	case <-time.After(workerOpTimeout):
		w.pendingMu.Lock()
		delete(w.pending, id)
		w.pendingMu.Unlock()
		return fmt.Errorf("send_feature_session timeout")
	}
}

// workerMain is the code path executed when the binary is started with
// --hid-worker. It listens on stdin for JSON commands and writes JSON
// responses to stdout.
func workerMain() {
	hid.Init()
	defer hid.Exit()
	dec := json.NewDecoder(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	// Top-level recover so we can emit a structured event with a stack
	// trace back to the parent process before the helper exits.
	defer func() {
		if r := recover(); r != nil {
			_ = enc.Encode(map[string]interface{}{"event": "worker_panic", "error": fmt.Sprintf("%v", r), "stack": string(debug.Stack())})
		}
	}()
	var writeMu sync.Mutex
	var sessionDev *hid.Device
	var sessionStop chan struct{}
	// Protects access to sessionDev/sessionStop/sessionDone
	var sessionMu sync.Mutex
	// Serializes HID operations (Read/GetFeature/SendFeature/Close)
	var sessionOpMu sync.Mutex
	// Closed when the active reader goroutine exits
	var sessionDone chan struct{}
	for {
		var req map[string]interface{}
		if err := dec.Decode(&req); err != nil {
			if err == io.EOF {
				return
			}
			// On decode errors, attempt to continue; report error
			writeMu.Lock()
			_ = enc.Encode(map[string]interface{}{"ok": false, "error": err.Error()})
			writeMu.Unlock()
			continue
		}
		cmd, _ := req["cmd"].(string)
		// capture id so we can include it in replies to match pending
		// request channels in the parent process.
		idv := req["id"]
		sendResp := func(m map[string]interface{}) {
			if idv != nil {
				m["id"] = idv
			}
			writeMu.Lock()
			_ = enc.Encode(m)
			writeMu.Unlock()
		}
		switch cmd {
		case "probe":
			path, _ := req["path"].(string)
			ridFloat, _ := req["reportId"].(float64)
			rid := byte(int(ridFloat))
			// Perform local open + GetFeatureReport scanning similar to the
			// in-process probe but isolated in the worker.
			go func(p string, r byte, idval interface{}) {
				lvl, chg, ok, usedLen, err := workerProbePath(p, r)
				resp := map[string]interface{}{}
				if err != nil {
					resp = map[string]interface{}{"ok": false, "error": err.Error()}
				} else if !ok {
					resp = map[string]interface{}{"ok": false}
				} else {
					resp = map[string]interface{}{"ok": true, "level": lvl, "charging": chg, "len": usedLen}
				}
				if idval != nil {
					resp["id"] = idval
				}
				writeMu.Lock()
				_ = enc.Encode(resp)
				writeMu.Unlock()
			}(path, rid, idv)
			// probe is handled asynchronously above
		case "probe_all":
			path, _ := req["path"].(string)
			// Iterate probeRIDs and return the first match.
			go func(p string, idval interface{}) {
				found := false
				for _, rid := range probeRIDs {
					lvl, chg, ok, usedLen, err := workerProbePath(p, rid)
					if err != nil {
						// If the worker sees an open/GetFeatureReport error,
						// treat it as inconclusive for this RID and continue.
						continue
					}
					if ok {
						resp := map[string]interface{}{"ok": true, "level": lvl, "charging": chg, "len": usedLen, "rid": int(rid)}
						if idval != nil {
							resp["id"] = idval
						}
						writeMu.Lock()
						_ = enc.Encode(resp)
						writeMu.Unlock()
						found = true
						break
					}
				}
				if !found {
					resp := map[string]interface{}{"ok": false}
					if idval != nil {
						resp["id"] = idval
					}
					writeMu.Lock()
					_ = enc.Encode(resp)
					writeMu.Unlock()
				}
			}(path, idv)
			// probe_all is handled asynchronously above
		case "poke":
			path, _ := req["path"].(string)
			ridFloat, _ := req["reportId"].(float64)
			rid := byte(int(ridFloat))
			bodyHex, _ := req["bodyHex"].(string)
			body, _ := hex.DecodeString(bodyHex)
			go func(p string, r byte, b []byte, idval interface{}) {
				if err := workerPokePath(p, r, b); err != nil {
					resp := map[string]interface{}{"ok": false, "error": err.Error()}
					if idval != nil {
						resp["id"] = idval
					}
					writeMu.Lock()
					_ = enc.Encode(resp)
					writeMu.Unlock()
				} else {
					resp := map[string]interface{}{"ok": true}
					if idval != nil {
						resp["id"] = idval
					}
					writeMu.Lock()
					_ = enc.Encode(resp)
					writeMu.Unlock()
				}
			}(path, rid, body, idv)

		case "open_session":
			path, _ := req["path"].(string)
			if path == "" {
				writeMu.Lock()
				_ = enc.Encode(map[string]interface{}{"ok": false, "error": "empty path"})
				writeMu.Unlock()
				continue
			}
			if sessionDev != nil {
				writeMu.Lock()
				_ = enc.Encode(map[string]interface{}{"ok": false, "error": "session already open"})
				writeMu.Unlock()
				continue
			}
			// Acknowledge the open request quickly and perform the
			// actual open asynchronously. This prevents the worker from
			// being tied up by a slow or blocked device open and allows
			// the main process to attach frame listeners while the
			// worker finishes initializing the session.
			// ACK to the caller that we've accepted the open_session
			// request so the parent can proceed; include the original id.
			sendResp(map[string]interface{}{"ok": true, "status": "opening"})

			go func(p string) {
				d, err := safeOpenPath(p)
				if err != nil {
					writeMu.Lock()
					_ = enc.Encode(map[string]interface{}{"event": "session_error", "error": fmt.Sprintf("open_device: %v", err)})
					writeMu.Unlock()
					return
				}
				done := make(chan struct{})
				// Publish session state under lock so other handlers can
				// observe it atomically.
				sessionMu.Lock()
				sessionDev = d
				sessionStop = make(chan struct{})
				sessionDone = done
				sessionMu.Unlock()

				// start input reader and stream frames as events. The
				// reader performs HID reads under sessionOpMu to avoid
				// concurrent cgo calls with other operations (GetFeature,
				// Close, etc.). When an error occurs the reader claims
				// and closes the session device so only one goroutine
				// performs the actual Close() call.
				go func(dev *hid.Device, stop chan struct{}, done chan struct{}, path string) {
					defer func() {
						// Signal reader completion so CloseSession can wait
						// for a clean shutdown instead of racing to Close().
						close(done)
					}()
					// Start with a conservative maximum buffer size and adapt if
					// the driver rejects the buffer. Some drivers return a quick
					// error when the requested buffer size is unsupported; in
					// that case attempt a few smaller sizes before abandoning
					// the session so adoption succeeds more often on odd drivers.
					preferredSizes := []int{65, 33, 16, 9}
					curIdx := 0
					buf := make([]byte, preferredSizes[curIdx])
					for {
						select {
						case <-stop:
							return
						default:
						}

						// Log attempted read size so the parent can correlate
						// low-level read errors with the buffer size used.
						writeMu.Lock()
						_ = enc.Encode(map[string]interface{}{"event": "log", "msg": fmt.Sprintf("[WORKER] session read attempt size=%d path=%s", preferredSizes[curIdx], path)})
						writeMu.Unlock()
						sessionOpMu.Lock()
						n, err := safeDeviceRead(dev, buf)
						sessionOpMu.Unlock()

						if err != nil {
							// If the read error looks like an unsupported
							// buffer size (e.g. 'supplied user buffer not valid'),
							// attempt a small set of fallback sizes before
							// failing the session. This helps on devices whose
							// read path rejects larger buffers but accepts a
							// smaller one.
							var fallbackOk bool
							if errStr := strings.ToLower(err.Error()); strings.Contains(errStr, "supplied user buffer") || strings.Contains(errStr, "invalid buffer") || strings.Contains(errStr, "invalid parameter") {
								writeMu.Lock()
								_ = enc.Encode(map[string]interface{}{"event": "log", "msg": fmt.Sprintf("[WORKER] session read error looks like invalid buffer (path=%s): %v — attempting smaller buffer fallbacks", path, err)})
								writeMu.Unlock()
								for i := 0; i < len(preferredSizes); i++ {
									if i == curIdx {
										continue
									}
									// Try a smaller size
									tryBuf := make([]byte, preferredSizes[i])
									writeMu.Lock()
									_ = enc.Encode(map[string]interface{}{"event": "log", "msg": fmt.Sprintf("[WORKER] session read fallback attempt size=%d path=%s", preferredSizes[i], path)})
									writeMu.Unlock()
									sessionOpMu.Lock()
									n2, err2 := safeDeviceRead(dev, tryBuf)
									sessionOpMu.Unlock()
									if err2 == nil {
										// Success — adopt this smaller buffer
										buf = tryBuf
										curIdx = i
										n = n2
										fallbackOk = true
										writeMu.Lock()
										_ = enc.Encode(map[string]interface{}{"event": "log", "msg": fmt.Sprintf("[WORKER] session read fallback succeeded size=%d path=%s n=%d", preferredSizes[i], path, n2)})
										writeMu.Unlock()
										break
									} else {
										writeMu.Lock()
										_ = enc.Encode(map[string]interface{}{"event": "log", "msg": fmt.Sprintf("[WORKER] session read fallback size=%d error: %v", preferredSizes[i], err2)})
										writeMu.Unlock()
									}
								}
							}
							if !fallbackOk {
								// Report the read error and close the session
								writeMu.Lock()
								_ = enc.Encode(map[string]interface{}{"event": "session_error", "error": err.Error()})
								writeMu.Unlock()

								// Claim the session state and ensure only the
								// claimer performs Close() to avoid concurrent
								// hid_close/hid_read races that trigger SIGSEGV.
								sessionMu.Lock()
								if sessionDev == dev {
									sessionDev = nil
									sessionStop = nil
									sessionDone = nil
									sessionMu.Unlock()
									sessionOpMu.Lock()
									func() {
										defer func() {
											if r := recover(); r != nil {
												// ignore
											}
										}()
										_ = dev.Close()
									}()
									sessionOpMu.Unlock()

									// Notify parent that the session has been closed
									writeMu.Lock()
									_ = enc.Encode(map[string]interface{}{"event": "session_closed", "path": path})
									writeMu.Unlock()
								} else {
									sessionMu.Unlock()
								}
								return
							}
						}
						if n > 0 {
							fh := hex.EncodeToString(buf[:n])
							writeMu.Lock()
							_ = enc.Encode(map[string]interface{}{"event": "frame", "frameHex": fh})
							writeMu.Unlock()
						}
					}
				}(d, sessionStop, done, p)
				// Signal that the session is ready so the client can
				// attempt feature reads or other operations.
				writeMu.Lock()
				_ = enc.Encode(map[string]interface{}{"event": "session_opened", "path": p})
				writeMu.Unlock()
			}(path)

		case "get_feature_session":
			ridFloat, _ := req["reportId"].(float64)
			rid := byte(int(ridFloat))
			// Snapshot the session device under sessionMu so we avoid a
			// race where the session is closed while we begin an
			// operation.
			sessionMu.Lock()
			dev := sessionDev
			sessionMu.Unlock()
			if dev == nil {
				sendResp(map[string]interface{}{"ok": false, "error": "no session"})
				continue
			}
			// Run the potentially-blocking feature reads inside a
			// goroutine and enforce a bounded timeout. Perform the
			// actual HID calls while holding sessionOpMu so they are
			// serialized with reads and closes. Emit a lightweight
			// 'log' event so the parent can see progress across sizes
			// and any low-level errors that occur during the attempts.
			// This helps diagnose why the helper returns a timeout.
			writeMu.Lock()
			_ = enc.Encode(map[string]interface{}{"event": "log", "msg": fmt.Sprintf("get_feature_session start rid=0x%02x", rid)})
			writeMu.Unlock()
			respCh := make(chan map[string]interface{}, 1)
			go func(dev *hid.Device, rid byte) {
				sizes := []int{65, 33, 16, 9}
				for _, sz := range sizes {
					buf := make([]byte, sz)
					buf[0] = rid
					// Emit a log event indicating we're attempting a
					// GetFeatureReport with this buffer size. This
					// helps diagnose which sizes cause 'incorrect
					// function' or other low-level errors.
					writeMu.Lock()
					_ = enc.Encode(map[string]interface{}{"event": "log", "msg": fmt.Sprintf("get_feature_session attempting size=%d", sz)})
					writeMu.Unlock()
					sessionOpMu.Lock()
					n, err := safeGetFeatureReport(dev, buf)
					sessionOpMu.Unlock()
					if err != nil {
						// Report the low-level error back to the parent
						// so it's visible in debug.log for analysis.
						writeMu.Lock()
						_ = enc.Encode(map[string]interface{}{"event": "log", "msg": fmt.Sprintf("get_feature_session size=%d error=%v", sz, err)})
						writeMu.Unlock()
						if strings.Contains(strings.ToLower(err.Error()), "incorrect function") {
							continue
						}
						respCh <- map[string]interface{}{"ok": false, "error": err.Error()}
						return
					}
					if n > 0 {
						writeMu.Lock()
						_ = enc.Encode(map[string]interface{}{"event": "log", "msg": fmt.Sprintf("get_feature_session size=%d n=%d", sz, n)})
						writeMu.Unlock()
						if lvl, chg, ok := parseBattery(buf[:n]); ok {
							respCh <- map[string]interface{}{"ok": true, "level": lvl, "charging": chg, "len": n}
							return
						}
						respCh <- map[string]interface{}{"ok": false}
						return
					}
				}
				respCh <- map[string]interface{}{"ok": false}
			}(dev, rid)

			select {
			case resp := <-respCh:
				sendResp(resp)
			case <-time.After(workerFeatureTimeout):
				sendResp(map[string]interface{}{"ok": false, "error": "get_feature_session timeout"})
			}

		case "send_feature_session":
			ridFloat, _ := req["reportId"].(float64)
			rid := byte(int(ridFloat))
			bodyHex, _ := req["bodyHex"].(string)
			body, _ := hex.DecodeString(bodyHex)
			sessionMu.Lock()
			dev := sessionDev
			sessionMu.Unlock()
			if dev == nil {
				sendResp(map[string]interface{}{"ok": false, "error": "no session"})
				continue
			}
			buf := make([]byte, 1+len(body))
			buf[0] = rid
			copy(buf[1:], body)
			sessionOpMu.Lock()
			if _, err := dev.SendFeatureReport(buf); err == nil {
				sessionOpMu.Unlock()
				sendResp(map[string]interface{}{"ok": true})
				continue
			}
			if _, err := dev.Write(buf); err == nil {
				sessionOpMu.Unlock()
				sendResp(map[string]interface{}{"ok": true})
				continue
			}
			sessionOpMu.Unlock()
			sendResp(map[string]interface{}{"ok": false, "error": "send failed"})

		case "close_session":
			// Snapshot session state so we can signal the reader and
			// wait for it to exit without racing on sessionDev.
			sessionMu.Lock()
			dev := sessionDev
			stop := sessionStop
			done := sessionDone
			sessionMu.Unlock()
			if dev == nil {
				sendResp(map[string]interface{}{"ok": false, "error": "no session"})
				continue
			}
			// Mark the stop channel as cleared under the sessionMu so
			// only one closer attempts to close it. Then close it to
			// signal the reader goroutine to exit.
			sessionMu.Lock()
			if sessionStop != nil {
				// consume/clear it so concurrent closers won't double-close
				sessionStop = nil
			}
			sessionMu.Unlock()
			if stop != nil {
				// Safe to close since we owned it above
				close(stop)
			}

			// Wait for the reader to finish cleanly and let it claim
			// and close the device. If the reader doesn't exit within
			// a bounded timeout, claim-and-close ourselves to avoid
			// leaking the handle.
			if done != nil {
				select {
				case <-done:
					// reader exited and should have closed device
					sendResp(map[string]interface{}{"ok": true})
					continue
				case <-time.After(workerOpTimeout):
					// Timed out — attempt to claim and close the session
					sessionMu.Lock()
					if sessionDev == dev {
						sessionDev = nil
						sessionStop = nil
						sessionDone = nil
						sessionMu.Unlock()
						sessionOpMu.Lock()
						func() {
							defer func() {
								if r := recover(); r != nil {
									writeMu.Lock()
									_ = enc.Encode(map[string]interface{}{"event": "session_error", "error": fmt.Sprintf("close panic: %v", r)})
									writeMu.Unlock()
								}
							}()
							_ = dev.Close()
						}()
						sessionOpMu.Unlock()
						writeMu.Lock()
						_ = enc.Encode(map[string]interface{}{"event": "session_closed"})
						writeMu.Unlock()
					} else {
						sessionMu.Unlock()
					}
					sendResp(map[string]interface{}{"ok": true})
					continue
				}
			}
			// If we have no done channel, just try to claim and close now.
			sessionMu.Lock()
			if sessionDev == dev {
				sessionDev = nil
				sessionStop = nil
				sessionDone = nil
				sessionMu.Unlock()
				sessionOpMu.Lock()
				func() {
					defer func() {
						if r := recover(); r != nil {
							writeMu.Lock()
							_ = enc.Encode(map[string]interface{}{"event": "session_error", "error": fmt.Sprintf("close panic: %v", r)})
							writeMu.Unlock()
						}
					}()
					_ = dev.Close()
				}()
				sessionOpMu.Unlock()
				writeMu.Lock()
				_ = enc.Encode(map[string]interface{}{"event": "session_closed"})
				writeMu.Unlock()
			} else {
				sessionMu.Unlock()
			}
			sendResp(map[string]interface{}{"ok": true})
		default:
			enc.Encode(map[string]interface{}{"ok": false, "error": "unknown command"})
		}
	}
}

func workerProbePath(path string, reportID byte) (int, bool, bool, int, error) {
	if path == "" {
		return 0, false, false, 0, fmt.Errorf("empty path")
	}
	d, err := safeOpenPath(path)
	if err != nil {
		return 0, false, false, 0, fmt.Errorf("open_device: %v", err)
	}
	defer d.Close()
	sizes := []int{65, 33, 16, 9}
	for _, sz := range sizes {
		// Perform the GetFeatureReport in a goroutine so we can bound
		// how long we wait for a potentially-blocking driver call.
		type resT struct {
			n   int
			err error
			buf []byte
		}
		resCh := make(chan resT, 1)
		go func(sz int) {
			buf := make([]byte, sz)
			buf[0] = reportID
			n, err := safeGetFeatureReport(d, buf)
			resCh <- resT{n: n, err: err, buf: buf}
		}(sz)

		select {
		case r := <-resCh:
			if r.err != nil {
				if strings.Contains(strings.ToLower(r.err.Error()), "incorrect function") {
					continue
				}
				return 0, false, false, 0, fmt.Errorf("GetFeatureReport err: %v", r.err)
			}
			if r.n > 0 {
				if lvl, chg, ok := parseBattery(r.buf[:r.n]); ok {
					return lvl, chg, true, sz, nil
				}
				return 0, false, false, 0, nil
			}
		case <-time.After(workerFeatureTimeout):
			// timed out for this size — treat as inconclusive and
			// continue to try the next size
			if logger != nil {
				logger.Printf("[WORKER] get_feature timeout for path=%s rid=0x%02x len=%d", path, reportID, sz)
			}
			continue
		}
	}
	return 0, false, false, 0, nil
}

func workerPokePath(path string, reportID byte, body []byte) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	d, err := safeOpenPath(path)
	if err != nil {
		return fmt.Errorf("open_device: %v", err)
	}
	defer d.Close()
	buf := make([]byte, 1+len(body))
	buf[0] = reportID
	copy(buf[1:], body)
	var pokeErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				pokeErr = fmt.Errorf("poke panic: %v", r)
				if logger != nil {
					logger.Printf("[WORKER] poke panic: %v", r)
				}
			}
		}()
		if _, e := d.SendFeatureReport(buf); e == nil {
			pokeErr = nil
			return
		}
		if _, e := d.Write(buf); e == nil {
			pokeErr = nil
			return
		}
		pokeErr = fmt.Errorf("SetFeature/Write failed")
	}()
	if pokeErr != nil {
		return pokeErr
	}
	return nil
}

func stringsContainsIgnoreCase(s, sub string) bool {
	return stringsContainsFold(s, sub)
}

// Use a very small helpers to avoid importing strings twice in worker.
func stringsContainsFold(s, sub string) bool {
	sLow := []rune(stringsToLower(s))
	subLow := []rune(stringsToLower(sub))
	return runesIndexOf(sLow, subLow) >= 0
}

func stringsToLower(s string) string { return string([]rune(strings.ToLower(s))) }

func runesIndexOf(a, b []rune) int {
	if len(b) == 0 {
		return 0
	}
	for i := 0; i+len(b) <= len(a); i++ {
		ok := true
		for j := range b {
			if a[i+j] != b[j] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}
