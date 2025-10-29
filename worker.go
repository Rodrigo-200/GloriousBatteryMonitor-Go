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
    workerProbeTimeout   = 8000 * time.Millisecond
    workerOpTimeout      = 10000 * time.Millisecond
    workerFeatureTimeout = 6000 * time.Millisecond
)

type WorkerClient struct {
    mu      sync.Mutex
    cmd     *exec.Cmd
    enc     *json.Encoder
    dec     *json.Decoder
    stdin   io.WriteCloser
    stdout  io.ReadCloser
    running bool

    pending   map[int]chan map[string]interface{}
    pendingMu sync.Mutex
    nextID    int

    events  chan map[string]interface{}
    frames  chan []byte
    writeMu sync.Mutex
}

var probeWorker *WorkerClient
var probeWorkerMu sync.Mutex
var workerExitMu sync.Mutex
var lastWorkerExit time.Time
var probeWorkerLastStart time.Time

func isD2W(path string) bool {
    var ok bool
    hid.Enumerate(0, 0, func(info *hid.DeviceInfo) error {
        if info.Path == path {
            ok = info.VendorID == 0x093A && info.ProductID == 0x824D
            return fmt.Errorf("found") // stop enumerate
        }
        return nil
    })
    return ok
}

func StartProbeWorker() error {
    probeWorkerMu.Lock()
    defer probeWorkerMu.Unlock()
    if probeWorker != nil {
        probeWorker.mu.Lock()
        running := probeWorker.running
        probeWorker.mu.Unlock()
        if running {
            return nil
        }
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
    stderrPipe, _ := cmd.StderrPipe()
    if err := cmd.Start(); err != nil {
        if logger != nil {
            logger.Printf("[WORKER] failed to start helper: %v", err)
        }
        return err
    }
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
    w.dec = json.NewDecoder(bufio.NewReader(stdout))
    w.pending = make(map[int]chan map[string]interface{})
    w.events = make(chan map[string]interface{}, 64)
    w.frames = make(chan []byte, 64)
    w.running = true
    if logger != nil {
        logger.Printf("[WORKER] helper started (pid=%d)", cmd.Process.Pid)
    }
    go func() {
        err := cmd.Wait()
        w.mu.Lock()
        w.running = false
        close(w.events)
        close(w.frames)
        w.pendingMu.Lock()
        for id, ch := range w.pending {
            close(ch)
            delete(w.pending, id)
        }
        w.pendingMu.Unlock()
        w.mu.Unlock()
        probeWorkerMu.Lock()
        if probeWorker == w {
            probeWorker = nil
        }
        probeWorkerMu.Unlock()
        workerExitMu.Lock()
        lastWorkerExit = time.Now()
        workerExitMu.Unlock()
        if logger != nil {
            logger.Printf("[WORKER] helper exited: %v", err)
        }
    }()
    go w.readLoop()
    return nil
}

func (w *WorkerClient) readLoop() {
    for {
        var msg map[string]interface{}
        if err := w.dec.Decode(&msg); err != nil {
            return
        }
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
            w.safeSendEvent(msg)
            continue
        }
    }
}

func safeSendPending(ch chan map[string]interface{}, msg map[string]interface{}) {
    defer func() {
        if r := recover(); r != nil {
        }
    }()
    ch <- msg
}

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
        w.pendingMu.Lock()
        delete(w.pending, id)
        w.pendingMu.Unlock()
        return 0, false, false, 0, fmt.Errorf("worker probe timeout")
    }
}

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
        w.pendingMu.Lock()
        delete(w.pending, id)
        w.pendingMu.Unlock()
        return fmt.Errorf("open_session ack timeout")
    }
}

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

func (w *WorkerClient) InputChannel() <-chan []byte { return w.frames }

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

func workerMain() {
    hid.Init()
    defer hid.Exit()
    dec := json.NewDecoder(os.Stdin)
    enc := json.NewEncoder(os.Stdout)
    defer func() {
        if r := recover(); r != nil {
            _ = enc.Encode(map[string]interface{}{"event": "worker_panic", "error": fmt.Sprintf("%v", r), "stack": string(debug.Stack())})
        }
    }()
    var writeMu sync.Mutex
    var sessionDev *hid.Device
    var sessionStop chan struct{}
    var sessionMu sync.Mutex
    var sessionOpMu sync.Mutex
    var sessionDone chan struct{}

    sendResp := func(m map[string]interface{}) {
        writeMu.Lock()
        _ = enc.Encode(m)
        writeMu.Unlock()
    }

    for {
        var req map[string]interface{}
        if err := dec.Decode(&req); err != nil {
            if err == io.EOF {
                return
            }
            sendResp(map[string]interface{}{"ok": false, "error": err.Error()})
            continue
        }
        cmd, _ := req["cmd"].(string)
        idv := req["id"]

        switch cmd {
        case "probe":
            path, _ := req["path"].(string)
            ridFloat, _ := req["reportId"].(float64)
            rid := byte(int(ridFloat))
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

        case "probe_all":
            path, _ := req["path"].(string)
            go func(p string, idval interface{}) {
                found := false
                for _, rid := range probeRIDs {
                    lvl, chg, ok, usedLen, err := workerProbePath(p, rid)
                    if err != nil {
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
                sendResp(map[string]interface{}{"ok": false, "error": "empty path"})
                continue
            }
            sessionMu.Lock()
            if sessionDev != nil {
                sessionMu.Unlock()
                sendResp(map[string]interface{}{"ok": false, "error": "session already open"})
                continue
            }
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
                sessionMu.Lock()
                sessionDev = d
                sessionStop = make(chan struct{})
                sessionDone = done
                sessionMu.Unlock()

                go func(dev *hid.Device, stop chan struct{}, done chan struct{}, path string) {
                    defer close(done)
                    preferredSizes := []int{65, 33, 16, 9}
                    curIdx := 0
                    buf := make([]byte, preferredSizes[curIdx])
                    for {
                        select {
                        case <-stop:
                            return
                        default:
                        }
                        writeMu.Lock()
                        _ = enc.Encode(map[string]interface{}{"event": "log", "msg": fmt.Sprintf("[WORKER] session read attempt size=%d path=%s", preferredSizes[curIdx], path)})
                        writeMu.Unlock()
                        sessionOpMu.Lock()
                        n, err := safeDeviceRead(dev, buf)
                        sessionOpMu.Unlock()
                        if err != nil {
                            var fallbackOk bool
                            errStr := strings.ToLower(err.Error())
                            if strings.Contains(errStr, "supplied user buffer") || strings.Contains(errStr, "invalid buffer") || strings.Contains(errStr, "invalid parameter") {
                                writeMu.Lock()
                                _ = enc.Encode(map[string]interface{}{"event": "log", "msg": fmt.Sprintf("[WORKER] session read error looks like invalid buffer (path=%s): %v — attempting smaller buffer fallbacks", path, err)})
                                writeMu.Unlock()
                                for i := 0; i < len(preferredSizes); i++ {
                                    if i == curIdx {
                                        continue
                                    }
                                    tryBuf := make([]byte, preferredSizes[i])
                                    writeMu.Lock()
                                    _ = enc.Encode(map[string]interface{}{"event": "log", "msg": fmt.Sprintf("[WORKER] session read fallback attempt size=%d path=%s", preferredSizes[i], path)})
                                    writeMu.Unlock()
                                    sessionOpMu.Lock()
                                    n2, err2 := safeDeviceRead(dev, tryBuf)
                                    sessionOpMu.Unlock()
                                    if err2 == nil {
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
                                writeMu.Lock()
                                _ = enc.Encode(map[string]interface{}{"event": "session_error", "error": err.Error()})
                                writeMu.Unlock()
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
                writeMu.Lock()
                _ = enc.Encode(map[string]interface{}{"event": "session_opened", "path": p})
                writeMu.Unlock()
            }(path)

        case "close_session":
            sessionMu.Lock()
            dev := sessionDev
            stop := sessionStop
            done := sessionDone
            sessionMu.Unlock()
            if dev == nil {
                sendResp(map[string]interface{}{"ok": false, "error": "no session"})
                continue
            }
            sessionMu.Lock()
            if sessionStop != nil {
                sessionStop = nil
            }
            sessionMu.Unlock()
            if stop != nil {
                close(stop)
            }
            if done != nil {
                select {
                case <-done:
                    sendResp(map[string]interface{}{"ok": true})
                    continue
                case <-time.After(workerOpTimeout):
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

        case "get_feature_session":
            ridFloat, _ := req["reportId"].(float64)
            rid := byte(int(ridFloat))
            sessionMu.Lock()
            dev := sessionDev
            sessionMu.Unlock()
            if dev == nil {
                sendResp(map[string]interface{}{"ok": false, "error": "no session"})
                continue
            }
            writeMu.Lock()
            _ = enc.Encode(map[string]interface{}{"event": "log", "msg": fmt.Sprintf("get_feature_session start rid=0x%02x", rid)})
            writeMu.Unlock()
            respCh := make(chan map[string]interface{}, 1)
            go func(dev *hid.Device, rid byte) {
                sizes := []int{65, 33, 16, 9}
                for _, sz := range sizes {
                    buf := make([]byte, sz)
                    buf[0] = rid
                    writeMu.Lock()
                    _ = enc.Encode(map[string]interface{}{"event": "log", "msg": fmt.Sprintf("get_feature_session attempting size=%d", sz)})
                    writeMu.Unlock()
                    sessionOpMu.Lock()
                    n, err := safeGetFeatureReport(dev, buf)
                    sessionOpMu.Unlock()
                    if err != nil {
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

        default:
            sendResp(map[string]interface{}{"ok": false, "error": "unknown command"})
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

    var deviceInfo *hid.DeviceInfo
    hid.Enumerate(0, 0, func(info *hid.DeviceInfo) error {
        if info.Path == path {
            deviceInfo = info
            return fmt.Errorf("found")
        }
        return nil
    })
    if deviceInfo != nil && settings.SafeMode {
        if !isDeviceWhitelisted(deviceInfo) {
            if logger != nil {
                logger.Printf("[WORKER:SAFE_MODE] Device not whitelisted, using read-only probe: VID:0x%04X PID:0x%04X", deviceInfo.VendorID, deviceInfo.ProductID)
            }
        }
    }

    // --------------------------------------------------
    // Model D 2 Wireless fast-path (0x093A:0x824D)
    // --------------------------------------------------
    if isD2W(path) && (!settings.SafeMode || (deviceInfo != nil && isDeviceWhitelisted(deviceInfo))) {
        if lvl, chg, ok := probeD2W(d); ok {
            return lvl, chg, true, 65, nil
        }
    }

    // --------------------------------------------------
    // Generic fallback for all other devices
    // --------------------------------------------------
    sizes := []int{65, 33, 16, 9}
    for _, sz := range sizes {
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
    return pokeErr
}

func probeD2W(dev *hid.Device) (int, bool, bool) {
    if logger != nil {
        logger.Printf("[WORKER] probeD2W: Starting Model D 2 Wireless probe (read-only)")
    }
    
    // Try Input reports first (read-only approach)
    buf := make([]byte, 65)
    
    // Try to read multiple times to get a battery report
    for attempt := 0; attempt < 5; attempt++ {
        n, err := dev.Read(buf)
        if err == nil && n > 0 {
            level, charging, valid := parseModelD2WirelessBatteryWorker(buf[:n])
            if valid {
                if logger != nil {
                    logger.Printf("[WORKER] probeD2W: Success via Input reports: level=%d charging=%v", level, charging)
                }
                return level, charging, true
            }
        }
        time.Sleep(100 * time.Millisecond)
    }
    
    // Fallback to read-only Feature report (no SendFeatureReport)
    if settings.SafeMode {
        if logger != nil {
            logger.Printf("[WORKER] probeD2W: SafeMode active, using read-only Feature report")
        }
    }
    
    buf = make([]byte, 65)
    n, err := dev.GetFeatureReport(buf)
    if err == nil && n > 0 {
        level, charging, valid := parseModelD2WirelessBatteryWorker(buf[:n])
        if valid {
            if logger != nil {
                logger.Printf("[WORKER] probeD2W: Success via Feature report: level=%d charging=%v", level, charging)
            }
            return level, charging, true
        }
    }
    
    if logger != nil {
        logger.Printf("[WORKER] probeD2W: All methods failed")
    }
    return -1, false, false
}

func parseModelD2WirelessBatteryWorker(data []byte) (int, bool, bool) {
    if len(data) < 8 {
        if logger != nil {
            logger.Printf("[WORKER:D2W] Report too short: %d bytes", len(data))
        }
        return -1, false, false
    }
    
    // Log raw data for debugging
    if logger != nil {
        logger.Printf("[WORKER:D2W] Raw report: %02x", data[:min(len(data), 16)])
    }
    
    // Try common Model D 2 Wireless report patterns
    // Pattern 1: Battery at offset 2, charging at offset 3
    if len(data) >= 4 {
        level := int(data[2])
        charging := data[3] == 0x01 || data[3] == 0x02
        if level >= 0 && level <= 100 {
            if logger != nil {
                logger.Printf("[WORKER:D2W] Pattern 1 success: level=%d charging=%v", level, charging)
            }
            return level, charging, true
        }
    }
    
    // Pattern 2: Battery at offset 3, charging flag at offset 4
    if len(data) >= 5 {
        level := int(data[3])
        charging := data[4] == 0x01 || data[4] == 0x02
        if level >= 0 && level <= 100 {
            if logger != nil {
                logger.Printf("[WORKER:D2W] Pattern 2 success: level=%d charging=%v", level, charging)
            }
            return level, charging, true
        }
    }
    
    // Pattern 3: Look for battery percentage in valid range anywhere in report
    for i := 0; i < len(data)-1; i++ {
        level := int(data[i])
        if level >= 0 && level <= 100 {
            // Check if next byte could be charging flag
            if i+1 < len(data) {
                charging := data[i+1] == 0x01 || data[i+1] == 0x02
                if logger != nil {
                    logger.Printf("[WORKER:D2W] Pattern 3 success: level=%d at offset %d charging=%v", level, i, charging)
                }
                return level, charging, true
            }
        }
    }
    
    if logger != nil {
        logger.Printf("[WORKER:D2W] No valid battery pattern found")
    }
    return -1, false, false
}
