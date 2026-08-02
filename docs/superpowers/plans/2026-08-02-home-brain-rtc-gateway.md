# Home Brain RTC Gateway Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a secrets-free Go/Pion and Node.js Lambda MVP that negotiates Alexa full-duplex Opus audio and records remote RTP directly as finalized OGG/Opus files.

**Architecture:** A standard-library Go HTTP server delegates signaling to a mutex-protected RTC session manager. Each Pion session owns its PeerConnection, silent local Opus track, RTCP reader, remote Opus recorder, and idempotent cleanup; Lambda translates Alexa directives into time-bounded HTTP calls. `SessionConnected` is observational only, and mixed video/audio offers preserve audio while rejecting video.

**Tech Stack:** Go 1.26, `github.com/pion/webrtc/v4` v4.2.17, Pion `oggwriter`, Node.js 24 ESM with `node:test`, Docker, Docker Compose, GitHub Actions.

## Global Constraints

- Use strict red-green-refactor TDD: every production behavior must first have a focused test that is run and observed failing for the expected reason.
- Use only Go, Pion WebRTC v4, Node.js 24, standard-library HTTP, Docker, and Docker Compose.
- Do not add STT, OpenAI, LLM, TTS, VAD, keyword detection, audio cleanup, WAV conversion, UI, database, Redis, Kubernetes, MediaMTX, TURN, or video processing.
- Use only IPv4/UDP ICE candidates and ports `UDP_PORT_MIN` through `UDP_PORT_MAX`.
- Publish `PUBLIC_IP` with `SetICEAddressRewriteRules`; never use deprecated `SetNAT1To1IPs`.
- Complete ICE gathering before returning the answer; do not use trickle ICE.
- Never log bearer/OAuth tokens, complete SDP, ICE passwords, or ICE username fragments.
- Limit HTTP request bodies to 1 MiB and answer generation to 4.5 seconds.
- `SessionConnected` must not gate negotiation, recording, state handling, TTL, or shutdown.
- Reject/inactivate offered video in SDP while continuing supported Opus audio.
- Duplicate session IDs return HTTP 409.
- The user manages Git; do not initialize Git or make commits.

---

## File Map

- `go.mod`, `go.sum`: Go module and pinned dependency graph.
- `internal/config/config.go`: environment parsing, defaults, public-IP and writable-directory validation.
- `internal/config/config_test.go`: configuration behavior.
- `internal/recording/recorder.go`: safe filename construction and idempotent OGG writer.
- `internal/recording/recorder_test.go`: traversal protection, OGG creation, finalization.
- `internal/rtc/server.go`: Pion MediaEngine/SettingEngine/API construction and SDP offer validation.
- `internal/rtc/session.go`: per-PeerConnection negotiation, track handling, RTCP consumption, state cleanup.
- `internal/rtc/session_manager.go`: duplicate protection, mutex map, connected marker, TTL, close-all.
- `internal/rtc/session_test.go`: real Pion offer/answer/media and concurrent lifecycle tests.
- `internal/httpapi/handler.go`: authenticated REST API and consistent JSON errors.
- `internal/httpapi/handler_test.go`: route, auth, validation, size limit, status mapping.
- `cmd/rtc-server/main.go`: dependency wiring, JSON logger, server timeouts, graceful shutdown.
- `lambda/index.mjs`: Alexa discovery, authorization, state, RTC directives, VPS client.
- `lambda/index.test.mjs`: Node directive and secret-safe logging tests.
- `lambda/package.json`: Node 24 ESM/test metadata.
- `Dockerfile`, `docker-compose.yml`: non-root image and VPS/Coolify deployment.
- `.env.example`, `.gitignore`, `recordings/.gitkeep`: secrets-free runtime setup.
- `.github/workflows/ci.yml`: formatting, vet, test, race, Lambda, and image build gates.
- `Makefile`: developer commands.
- `README.md`: architecture, deployment, security, operation, and troubleshooting.

---

### Task 1: Go Module and Validated Configuration

**Files:**
- Create: `go.mod`
- Create: `internal/config/config_test.go`
- Create: `internal/config/config.go`

**Interfaces:**
- Produces: `config.Config`, `config.Load(getenv func(string) string) (Config, error)`.
- `Config` fields: `HTTPAddr string`, `PublicIP net.IP`, `UDPPortMin uint16`, `UDPPortMax uint16`, `SessionAPIToken string`, `RecordingsDir string`, `SessionTTL time.Duration`, `LogLevel slog.Level`.

- [ ] **Step 1: Create the module and write failing configuration tests**

```go
func TestLoadValidConfig(t *testing.T) {
    dir := t.TempDir()
    env := map[string]string{
        "PUBLIC_IP": "8.8.8.8", "SESSION_API_TOKEN": "secret",
        "RECORDINGS_DIR": dir, "UDP_PORT_MIN": "41000",
        "UDP_PORT_MAX": "41010", "SESSION_TTL": "2m",
    }
    got, err := Load(func(k string) string { return env[k] })
    if err != nil { t.Fatal(err) }
    if got.HTTPAddr != ":8080" || got.UDPPortMin != 41000 || got.SessionTTL != 2*time.Minute {
        t.Fatalf("unexpected config: %#v", got)
    }
}

func TestLoadRejectsInvalidValues(t *testing.T) {
    cases := []struct{ name, key, value string }{
        {"private IP", "PUBLIC_IP", "10.0.0.1"},
        {"IPv6", "PUBLIC_IP", "2001:4860:4860::8888"},
        {"reversed ports", "UDP_PORT_MIN", "50000"},
        {"empty token", "SESSION_API_TOKEN", ""},
    }
    // Each case starts from a valid literal environment and asserts a non-nil error.
}
```

- [ ] **Step 2: Run the configuration tests and verify RED**

Run: `go test ./internal/config -run TestLoad -v`  
Expected: FAIL because `Load` and `Config` do not exist.

- [ ] **Step 3: Implement minimal parsing and validation**

```go
func Load(getenv func(string) string) (Config, error) {
    cfg := Config{HTTPAddr: valueOr(getenv("HTTP_ADDR"), ":8080")}
    // Parse exact documented defaults, validate uint16 range/order and duration,
    // require token, reject non-public IPv4, mkdir recordings, and probe writes.
    return cfg, nil
}
```

Public IPv4 validation will reject all IANA special-purpose ranges using explicit `netip.Prefix` literals, not only `IP.IsPrivate()`.

- [ ] **Step 4: Run config tests and refactor while green**

Run: `go test ./internal/config -v`  
Expected: PASS.

---

### Task 2: Safe, Idempotent OGG/Opus Recorder

**Files:**
- Create: `internal/recording/recorder_test.go`
- Create: `internal/recording/recorder.go`
- Update: `go.mod`
- Generate: `go.sum`

**Interfaces:**
- Produces: `recording.Factory` interface with `New(sessionID string) (Recorder, error)`.
- Produces: `recording.Recorder` interface with `WriteRTP(*rtp.Packet) error`, `Close() error`, `Path() string`.
- Produces: `recording.NewFactory(dir string, now func() time.Time) *OGGFactory`.

- [ ] **Step 1: Write failing path-safety and OGG behavior tests**

```go
func TestFactoryKeepsRecordingInsideDirectory(t *testing.T) {
    dir := t.TempDir()
    f := NewFactory(dir, func() time.Time { return time.Date(2026, 8, 2, 12, 0, 0, 123, time.UTC) })
    rec, err := f.New("../../escape/session")
    if err != nil { t.Fatal(err) }
    defer rec.Close()
    rel, err := filepath.Rel(dir, rec.Path())
    if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
        t.Fatalf("unsafe path %q", rec.Path())
    }
}

func TestRecorderWritesOggHeaderAndClosesTwice(t *testing.T) {
    rec, _ := NewFactory(t.TempDir(), fixedTime).New("session-1")
    pkt := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: 1, Timestamp: 960, SSRC: 1234}, Payload: []byte{0xF8, 0xFF, 0xFE}}
    if err := rec.WriteRTP(pkt); err != nil { t.Fatal(err) }
    if err := rec.Close(); err != nil { t.Fatal(err) }
    if err := rec.Close(); err != nil { t.Fatal(err) }
    data, _ := os.ReadFile(rec.Path())
    if !bytes.HasPrefix(data, []byte("OggS")) { t.Fatalf("missing OggS header") }
}
```

- [ ] **Step 2: Run recorder tests and verify RED**

Run: `go test ./internal/recording -v`  
Expected: FAIL because factory and recorder types do not exist.

- [ ] **Step 3: Add Pion dependency and minimal recorder**

Run: `go get github.com/pion/webrtc/v4@v4.2.17`

```go
type oggRecorder struct {
    mu sync.Mutex
    path string
    writer *oggwriter.OggWriter
    closed bool
}

func (r *oggRecorder) WriteRTP(pkt *rtp.Packet) error {
    r.mu.Lock(); defer r.mu.Unlock()
    if r.closed { return ErrClosed }
    return r.writer.WriteRTP(pkt)
}
```

Use `oggwriter.New(path, 48000, 2)`, a UTC nanosecond timestamp, a filename-safe session component, `filepath.Rel` containment verification, and mutex-protected idempotent close.

- [ ] **Step 4: Run recorder tests and tidy dependencies**

Run: `go test ./internal/recording -v`  
Run: `go mod tidy`  
Expected: PASS and a generated `go.sum`.

---

### Task 3: Pion Server, SDP Validation, and Complete Answer Generation

**Files:**
- Create: `internal/rtc/session_test.go`
- Create: `internal/rtc/server.go`
- Create: `internal/rtc/session.go`

**Interfaces:**
- Produces: `rtc.Options{PublicIP net.IP, UDPPortMin, UDPPortMax uint16, Recordings recording.Factory, Logger *slog.Logger, AnswerTimeout time.Duration, DisconnectGrace time.Duration}`.
- Produces: `rtc.NewServer(Options) (*Server, error)` and `(*Server).NewSession(ctx context.Context, id, offer string, onTerminal func()) (*Session, string, error)`.
- Produces sentinel errors `ErrInvalidSDP`, `ErrOpusRequired`, `ErrAnswerTimeout`.
- Produces `(*Session).Close() error`, `(*Session).MarkConnected()`, `(*Session).Connected() bool`.

- [ ] **Step 1: Write failing real-Pion SDP tests**

Create test helpers that build an offerer with a local Opus `TrackLocalStaticRTP`, gather its ICE fully, and return offer SDP plus the peer/track for later RTP sending.

```go
func TestNewSessionGeneratesCompleteOpusAnswer(t *testing.T) {
    offerer, _, offer := newOpusOfferer(t, false)
    defer offerer.Close()
    srv := newTestServer(t)
    session, answer, err := srv.NewSession(context.Background(), "s1", offer, func(){})
    if err != nil { t.Fatal(err) }
    defer session.Close()
    for _, want := range []string{"m=audio", "opus/48000", "a=candidate:", "a=rtcp-mux", "a=group:BUNDLE"} {
        if !strings.Contains(strings.ToLower(answer), strings.ToLower(want)) { t.Errorf("answer missing %q", want) }
    }
}

func TestNewSessionKeepsAudioWhenOfferContainsVideo(t *testing.T) {
    _, _, offer := newOpusOfferer(t, true)
    _, answer, err := newTestServer(t).NewSession(context.Background(), "s2", offer, func(){})
    if err != nil { t.Fatal(err) }
    // Parse SDP: audio port is non-zero and video port is zero or direction inactive.
    assertAudioAcceptedVideoRejected(t, answer)
}
```

Add tests for malformed SDP, audio without Opus, and a canceled/deadline context yielding `ErrAnswerTimeout`.

- [ ] **Step 2: Run RTC negotiation tests and verify RED**

Run: `go test ./internal/rtc -run 'TestNewSession' -v`  
Expected: FAIL because RTC server/session do not exist.

- [ ] **Step 3: Implement Pion API construction and session negotiation**

```go
se := webrtc.SettingEngine{}
se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
if err := se.SetEphemeralUDPPortRange(opts.UDPPortMin, opts.UDPPortMax); err != nil { return nil, err }
if err := se.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
    External: []string{opts.PublicIP.String()},
    AsCandidateType: webrtc.ICECandidateTypeHost,
    Mode: webrtc.ICEAddressRewriteReplace,
    Networks: []webrtc.NetworkType{webrtc.NetworkTypeUDP4},
}); err != nil { return nil, err }
```

Register only Opus audio in a `MediaEngine`. Before `SetRemoteDescription`, parse the offer with `github.com/pion/sdp/v3` to require an audio media section containing `opus/48000`; allow video sections to remain unsupported. Add a local Opus track, start `ReadRTCP`, set remote description, create/set answer, then select between `GatheringCompletePromise(pc)` and the request deadline. Return `pc.LocalDescription().SDP`, never the pre-gather answer.

- [ ] **Step 4: Run negotiation tests and refactor while green**

Run: `go test ./internal/rtc -run 'TestNewSession' -v`  
Expected: PASS.

---

### Task 4: Recording From OnTrack and Race-Safe Session Lifecycle

**Files:**
- Modify: `internal/rtc/session_test.go`
- Modify: `internal/rtc/session.go`
- Create: `internal/rtc/session_manager.go`

**Interfaces:**
- Produces: `rtc.Manager` with `Create(ctx, id, offer) (answer string, error)`, `MarkConnected(id) error`, `Close(id) error`, `CloseAll() error`, `Len() int`.
- Produces sentinel errors `ErrSessionExists`, `ErrSessionNotFound`.
- `rtc.NewManager(server *Server, ttl time.Duration, logger *slog.Logger) *Manager`.

- [ ] **Step 1: Write failing lifecycle and media tests**

```go
func TestRemoteOpusCreatesOggWithoutConnectedDirective(t *testing.T) {
    offerer, track, offer := newOpusOfferer(t, false)
    mgr := newTestManager(t, time.Minute)
    answer, err := mgr.Create(context.Background(), "no-connected", offer)
    if err != nil { t.Fatal(err) }
    connectAnswer(t, offerer, answer)
    waitConnected(t, offerer)
    if err := track.WriteRTP(testOpusPacket(1)); err != nil { t.Fatal(err) }
    path := waitForRecording(t, recordingsDir(t))
    if err := mgr.Close("no-connected"); err != nil { t.Fatal(err) }
    data, _ := os.ReadFile(path)
    if !bytes.HasPrefix(data, []byte("OggS")) { t.Fatal("recording not finalized") }
}
```

Add tests that duplicate create returns `ErrSessionExists`, mark-connected is idempotent, close releases resources and removes the map entry, short TTL removes a session, concurrent create/mark/close does not race, and disconnect grace cancels on reconnection or closes after five seconds using an injected timer/short test duration.

- [ ] **Step 2: Run lifecycle/media tests and verify RED**

Run: `go test ./internal/rtc -run 'TestRemoteOpus|TestManager|TestSession' -v`  
Expected: FAIL because manager and OnTrack recorder lifecycle are absent.

- [ ] **Step 3: Implement OnTrack and manager lifecycle**

```go
pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
    if !strings.EqualFold(track.Codec().MimeType, webrtc.MimeTypeOpus) { return }
    if !session.claimRemoteAudio() { return }
    go session.recordTrack(track)
})
```

Use `sync.Once` for close, separate mutexes for connected/recorder state, a manager mutex for the map, `time.AfterFunc` for TTL, and a terminal callback that removes only the same session pointer. PeerConnection failed/closed close immediately; disconnected starts the configured grace timer and connected cancels it. All goroutines exit after PeerConnection or session context closure.

- [ ] **Step 4: Run lifecycle tests, package tests, and race detector**

Run: `go test ./internal/rtc -v`  
Run: `go test -race ./internal/rtc -v`  
Expected: PASS with no race report.

---

### Task 5: Authenticated Standard-Library HTTP API

**Files:**
- Create: `internal/httpapi/handler_test.go`
- Create: `internal/httpapi/handler.go`

**Interfaces:**
- Consumes: an internal `SessionService` interface matching manager create/mark/close.
- Produces: `httpapi.New(token string, sessions SessionService, logger *slog.Logger) http.Handler`.
- Produces consistent JSON `{"error":{"code":"...","message":"..."}}`.

- [ ] **Step 1: Write failing HTTP contract tests**

Use an in-memory fake only at the RTC boundary and assert handler responses, not fake call counts.

```go
func TestHealthIsPublic(t *testing.T) {
    rr := httptest.NewRecorder()
    New("secret", fakeSessions{}, discardLogger()).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
    if rr.Code != 200 || rr.Body.String() != "{\"status\":\"ok\"}\n" { t.Fatalf("%d %s", rr.Code, rr.Body.String()) }
}

func TestV1RejectsWrongBearerToken(t *testing.T) {
    req := httptest.NewRequest(http.MethodPost, "/v1/rtc/sessions", strings.NewReader(`{"sessionId":"s","offerSdp":"v=0"}`))
    req.Header.Set("Authorization", "Bearer wrong")
    rr := httptest.NewRecorder(); New("secret", fakeSessions{}, discardLogger()).ServeHTTP(rr, req)
    if rr.Code != http.StatusUnauthorized { t.Fatalf("got %d", rr.Code) }
}
```

Add cases for missing auth, missing session ID, malformed/invalid SDP mapping, duplicate 409, not found 404, answer timeout 504, 1 MiB overflow 413, successful create, connected, delete, method rejection, and path-escaped session IDs.

- [ ] **Step 2: Run handler tests and verify RED**

Run: `go test ./internal/httpapi -v`  
Expected: FAIL because handler does not exist.

- [ ] **Step 3: Implement routes, constant-time auth, and JSON errors**

Use Go 1.22+ `http.ServeMux` patterns, `http.MaxBytesReader`, `json.Decoder.DisallowUnknownFields`, a second decode to require EOF, `subtle.ConstantTimeCompare`, `url.PathUnescape`, and exact method/path patterns. Do not log request headers or bodies.

- [ ] **Step 4: Run HTTP tests and all Go tests**

Run: `go test ./internal/httpapi -v`  
Run: `go test ./...`  
Expected: PASS.

---

### Task 6: Alexa Lambda Directive Adapter

**Files:**
- Create: `lambda/package.json`
- Create: `lambda/index.test.mjs`
- Create: `lambda/index.mjs`

**Interfaces:**
- Produces AWS entrypoint `export async function handler(request)`.
- Produces test seam `export function createHandler({ fetchImpl, logger, env, randomUUID })`.
- Consumes `RTC_SERVER_URL`, `RTC_SERVER_TOKEN`.

- [ ] **Step 1: Write failing Node directive tests**

```js
test('InitiateSessionWithOffer returns the VPS answer and preserves routing metadata', async () => {
  const fetchImpl = async () => new Response(JSON.stringify({sessionId: 's-1', answerSdp: 'v=0\r\nanswer'}), {status: 200});
  const handler = createHandler({fetchImpl, logger: silentLogger, env: validEnv, randomUUID: () => 'message-1'});
  const result = await handler(initiateDirective('s-1', 'v=0\r\noffer'));
  assert.equal(result.event.header.name, 'AnswerGeneratedForSession');
  assert.equal(result.event.header.correlationToken, 'correlation-1');
  assert.equal(result.event.payload.answer.value, 'v=0\r\nanswer');
});
```

Add tests for Discover capability literals, AcceptGrant, ReportState, 4.8-second abort behavior using an injected never-resolving fetch and short injectable timeout, VPS 500, SessionConnected POST, SessionDisconnected DELETE, preservation of scope/endpoint/correlation/session ID, and captured logs excluding sentinel SDP/token strings.

- [ ] **Step 2: Run Lambda tests and verify RED**

Run: `node --test lambda/index.test.mjs`  
Expected: FAIL because `index.mjs` exports do not exist.

- [ ] **Step 3: Implement the minimal directive router and VPS client**

```js
async function rtcRequest(path, {method = 'POST', body, timeoutMs = 4800} = {}) {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const response = await fetchImpl(`${env.RTC_SERVER_URL}${path}`, {
      method, signal: controller.signal,
      headers: {'authorization': `Bearer ${env.RTC_SERVER_TOKEN}`, 'content-type': 'application/json'},
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    if (!response.ok) throw new Error('rtc server request failed');
    return response.status === 204 ? undefined : response.json();
  } finally { clearTimeout(timer); }
}
```

Log only safe directive metadata. Map all RTC/network failures to Alexa `ENDPOINT_UNREACHABLE`. Build directive-specific events with exact Alexa v3 headers and preserved endpoint scope.

- [ ] **Step 4: Run Lambda tests**

Run: `node --test lambda/index.test.mjs`  
Expected: PASS.

---

### Task 7: Runtime Wiring, Container, CI, and Operations Documentation

**Files:**
- Create: `cmd/rtc-server/main.go`
- Create: `.env.example`
- Create: `.gitignore`
- Create: `recordings/.gitkeep`
- Create: `Dockerfile`
- Create: `docker-compose.yml`
- Create: `.github/workflows/ci.yml`
- Create: `Makefile`
- Create: `README.md`

**Interfaces:**
- `main` loads config, constructs recorder/server/manager/handler, starts `http.Server`, and closes sessions on SIGINT/SIGTERM.
- Container exposes TCP 8080, runs non-root, and writes `/data/recordings`.

- [ ] **Step 1: Write/extend failing process-level tests before runtime code**

Add a handler shutdown test using a real manager with an injected closable session and a config test proving an unwritable/non-directory recordings path prevents startup. Run them before creating `main.go`.

Run: `go test ./...`  
Expected: FAIL for the newly asserted close/startup behavior.

- [ ] **Step 2: Implement runtime wiring and structured logging**

```go
srv := &http.Server{
    Addr: cfg.HTTPAddr, Handler: handler,
    ReadHeaderTimeout: 5*time.Second, ReadTimeout: 10*time.Second,
    WriteTimeout: 6*time.Second, IdleTimeout: 60*time.Second,
}
```

Use `slog.NewJSONHandler`, a signal context, bounded `Shutdown`, then `manager.CloseAll()`. Exit non-zero on invalid config or server construction.

- [ ] **Step 3: Run Go tests and build the binary**

Run: `go test ./...`  
Run: `go build ./cmd/rtc-server`  
Expected: PASS.

- [ ] **Step 4: Create secrets-free deployment and CI files**

Use `golang:1.26-alpine` build stage and an Alpine runtime with `ca-certificates`, `wget`, a numeric non-root user, `/data/recordings` ownership, TCP expose, and an exec-form entrypoint. Compose publishes exact TCP/UDP ranges, mounts `./recordings`, uses `.env`, and healthchecks `/healthz`. `.env.example` uses the reserved documentation address `PUBLIC_IP=203.0.113.10` and `SESSION_API_TOKEN=replace-with-openssl-rand-hex-32`, never a real secret.

CI uses Go 1.26 and Node 24 and runs every required gate. Make targets are `fmt`, `vet`, `test`, `test-race`, `test-lambda`, `build`, and `verify`.

- [ ] **Step 5: Write complete README**

Document architecture, signaling/media flow, environment table, token creation, UFW/cloud firewall, Compose setup, health check, Lambda variables/deploy, Alexa debugger, voice command, logs, recordings, playback, 409 duplicate behavior, optional SessionConnected, video rejection with audio continuation, private-HTTP warning, and every troubleshooting item from the approved design.

- [ ] **Step 6: Validate generated operational files**

Run: `docker compose config`  
Expected: exit 0 using a temporary copied `.env.example` as `.env`; remove only that generated `.env` after validation.

Run: `docker build .`  
Expected: exit 0 and non-root runtime image produced.

---

### Task 8: Full Verification, Security Audit, and Handoff

**Files:**
- Modify only files implicated by failing verification.

**Interfaces:**
- Produces a truthful command result report and secrets-free repository tree.

- [ ] **Step 1: Format and inspect the repository**

Run: `gofmt -w cmd internal`  
Run: `test -z "$(gofmt -l cmd internal)"`  
Run: `go vet ./...`

- [ ] **Step 2: Run fresh complete Go verification**

Run: `go test ./...`  
Run: `go test -race ./...`  
Expected: both exit 0 with no failed test and no race report.

- [ ] **Step 3: Run fresh Lambda verification**

Run: `node --test lambda/index.test.mjs`  
Expected: exit 0 with zero failed tests.

- [ ] **Step 4: Run fresh container verification**

Run: `docker build .`  
Run: `docker compose config`  
Expected: both exit 0. If Docker is unavailable, report the exact blocker and do not claim completion.

- [ ] **Step 5: Audit secrets and scope**

Run targeted searches for bearer values, private keys, AWS keys, real-looking public IP assignments, complete logged SDP, and excluded feature dependencies. Inspect every match manually. Confirm `.env` is ignored and absent, while `.env.example` contains only documentation values.

- [ ] **Step 6: Request code review and resolve findings**

Use `superpowers:requesting-code-review` against the approved design and this plan. Because the workspace is intentionally not a Git repository, provide the reviewer the full tree and requirements instead of Git SHAs. Fix every Critical/Important finding with a new failing test first, then rerun affected and full verification.

- [ ] **Step 7: Deliver the handoff**

Report the created tree, exact Pion settings, every verification command with real exit result, `docker compose up -d --build`, Lambda path `lambda/index.mjs`, known real-device/NAT risks, and secrets audit status. Never call the work complete if race tests, Lambda tests, Docker build, or Compose validation fail.
