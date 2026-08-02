# RTC Outbound Opus Audio Priming Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an opt-in diagnostic that sends valid 20 ms Opus silence samples on the existing negotiated local audio track after the PeerConnection reaches connected state.

**Architecture:** Replace the existing unwritten `TrackLocalStaticRTP` with an SDP-equivalent `TrackLocalStaticSample`, then give each session a single guarded audio-prime runner. The runner is driven by an injectable ticker, checks cancellation again after every received tick, and writes the fixed Opus silence packet only while the opt-in configuration is enabled.

**Tech Stack:** Go 1.26, Pion WebRTC v4, `pion/webrtc/pkg/media`, Docker

## Global Constraints

- `RTC_AUDIO_PRIME_ENABLED` defaults to `false`.
- `RTC_AUDIO_PRIME_DURATION` defaults to `10s` and must be a positive multiple of `20ms`.
- Use the existing negotiated local Opus track and payload type 111; do not add an audio m-line, transceiver, codec, or active video path.
- Start only after `PeerConnectionStateConnected` and at most once per session.
- Write only the pre-encoded Opus silence payload `F8 FF FE` as `media.Sample{Duration: 20ms}`.
- Stop on duration, session cancellation, PeerConnection closed/failed, or the first write error.
- If cancellation and a tick are both ready, check `ctx.Err()` after receiving the tick and before `WriteSample`.
- Safe logs are limited to `audio_prime_started`, `audio_prime_completed frames=<count> reason=<duration|cancelled>`, and `audio_prime_failed category=write`, plus the existing safe session ID correlation field.
- Do not alter SDP normalization, Lambda, video negotiation, ICE, codec registration, recording, or Docker behavior.
- The local integration test must use the real `probeOptions{includeVideo: true}` mixed-offer path.

---

### Task 1: Typed configuration and wiring

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/rtc-server/main.go`
- Modify: `.env.example`

**Interfaces:**
- Produces: `Config.AudioPrimeEnabled bool`
- Produces: `Config.AudioPrimeDuration time.Duration`
- Produces: `rtc.Options.AudioPrimeEnabled bool`
- Produces: `rtc.Options.AudioPrimeDuration time.Duration`

- [ ] **Step 1: Add configuration RED tests**

Add tests requiring disabled/10s defaults, explicit `true` and `40ms` parsing, and rejection of invalid booleans, zero/negative durations, durations below 20ms, and durations not divisible by 20ms.

- [ ] **Step 2: Run focused config tests and verify RED**

Run:

```bash
go test ./internal/config -run 'AudioPrime|LoadAppliesDefaults' -count=1
```

Expected: compile or assertion failure because the typed fields and parsing do not exist.

- [ ] **Step 3: Implement minimum config parsing and server wiring**

Add constants for `10*time.Second` and `20*time.Millisecond`, parse `RTC_AUDIO_PRIME_ENABLED` with the existing boolean parser, parse the duration with `time.ParseDuration`, enforce the duration constraints, and pass both values into `rtc.NewServer`.

- [ ] **Step 4: Update `.env.example`**

Add:

```dotenv
RTC_AUDIO_PRIME_ENABLED=false
RTC_AUDIO_PRIME_DURATION=10s
```

Do not modify Compose because its existing `env_file: .env` passes both variables through.

- [ ] **Step 5: Run focused config and server tests GREEN**

Run:

```bash
go test ./internal/config ./cmd/rtc-server -count=1
```

Expected: all tests pass.

---

### Task 2: Deterministic Opus prime runner

**Files:**
- Create: `internal/rtc/audio_prime.go`
- Create: `internal/rtc/audio_prime_test.go`

**Interfaces:**
- Produces: `audioPrimeWriter` with `WriteSample(media.Sample) error`
- Produces: `audioPrimeTicker` with `C() <-chan time.Time` and `Stop()`
- Produces: `runAudioPrime(context.Context, audioPrimeWriter, audioPrimeTicker, int) (frames int, reason audioPrimeCompletionReason, err error)`
- Produces: fixed enum values `audioPrimeReasonDuration` and `audioPrimeReasonCancelled`

- [ ] **Step 1: Add runner RED tests**

Use a manual ticker channel and recording writer to require exactly three 20ms samples for three ticks, exact `F8 FF FE` payload bytes, duration completion reason, and ticker stop.

- [ ] **Step 2: Add deterministic cancellation RED test**

Queue a tick, cancel the context before the runner handles it, and require zero writes and `cancelled`. Also cancel after one successful write, queue another tick, and require the count to remain one.

- [ ] **Step 3: Add write-failure RED test**

Make the writer fail on its first call and require immediate return with the write error and no later writes.

- [ ] **Step 4: Run focused runner tests and verify RED**

Run:

```bash
go test ./internal/rtc -run '^TestRunAudioPrime' -count=1
```

Expected: compile failure because the runner does not exist.

- [ ] **Step 5: Implement the minimum runner**

Use a loop bounded by target frame count. Select on context and ticker; immediately after the ticker case, test `ctx.Err()` before constructing or writing the sample. Return `duration` only after all target frames and `cancelled` for context termination. Never log or retry inside the runner.

- [ ] **Step 6: Run focused runner tests GREEN**

Run:

```bash
go test ./internal/rtc -run '^TestRunAudioPrime' -count=1
```

Expected: all runner tests pass.

---

### Task 3: Session lifecycle and existing-track integration

**Files:**
- Modify: `internal/rtc/server.go`
- Modify: `internal/rtc/session.go`
- Modify: `internal/rtc/session_test.go`
- Modify: `internal/rtc/audio_prime_test.go`

**Interfaces:**
- Extends: `rtc.Options` and `Server` with audio-prime enabled/duration fields
- Extends: `Session` with the existing local sample writer, a ticker factory, a prime `sync.Once`, and a prime-specific cancel function
- Produces: `startAudioPrime()` and `stopAudioPrime()` session lifecycle methods

- [ ] **Step 1: Add session lifecycle RED tests**

Require disabled mode to produce no writer calls or start log; connecting/disconnected states to produce no start; connected to start exactly once; repeated connected transitions to keep one ticker/goroutine; cancellation and closed state to complete with `reason=cancelled`; duration to complete with `reason=duration`; and write failure to emit only `audio_prime_failed category=write` without closing the session.

- [ ] **Step 2: Add disabled-mode SDP RED/characterization test**

Generate an answer with priming disabled and require the existing single Opus audio m-line, payload 111 mapping, direction, and codec information to remain unchanged, with no outbound writer invocation.

- [ ] **Step 3: Run focused lifecycle tests and verify RED**

Run:

```bash
go test ./internal/rtc -run 'AudioPrime|DisabledAudioPrime' -count=1
```

Expected: compile or assertion failure because the session fields and lifecycle hooks do not exist.

- [ ] **Step 4: Implement the minimum lifecycle integration**

Create the existing local audio track as `webrtc.NewTrackLocalStaticSample` using the unchanged Opus capability, track ID, and stream ID. Store it on the session, start via `sync.Once` only from `PeerConnectionStateConnected`, stop its derived context synchronously for closed/failed and from `Session.Close`, and log only the specified stable events and fields.

- [ ] **Step 5: Run focused lifecycle and existing RTC tests GREEN**

Run:

```bash
go test ./internal/rtc -count=1
```

Expected: all existing recording/session/normalization tests and new lifecycle tests pass.

---

### Task 4: Production Alexa mixed-offer Pion loopback regression

**Files:**
- Modify: `cmd/rtc-probe/main_test.go`

**Interfaces:**
- Consumes: `newPionProbe(probeOptions{includeVideo: true}, io.Discard)`
- Consumes: `rtc.NewServer` with a short `AudioPrimeDuration` of `40ms`
- Consumes: production `Server.NewSession` and normalization chain without test-side SDP mutation

- [ ] **Step 1: Add mixed-offer loopback RED test**

Create the real probe mixed offer, register `OnTrack` before applying the answer, call `Server.NewSession`, and require the answer to contain exactly one accepted audio m-line and a rejected video m-line with port 0. Apply the untouched answer and wait for two remote RTP packets.

- [ ] **Step 2: Assert RTP and negotiation behavior**

Require both packet payload types to equal 111, both payloads to equal `F8 FF FE`, the second timestamp minus the first to equal 960, the answer audio m-line count to equal one, and the answer video m-line port to equal zero.

- [ ] **Step 3: Run the focused integration test and verify RED**

Run:

```bash
go test ./cmd/rtc-probe -run '^TestMixedOfferProductionAudioPrimeProducesOpusRTP$' -count=1
```

Expected: compile or timeout failure because audio priming is not wired.

- [ ] **Step 4: Complete only the minimum integration wiring needed for GREEN**

Pass the enabled/40ms options into the real RTC server and use the production answer without an extra normalizer, fixture SDP, track, or transceiver.

- [ ] **Step 5: Run the focused integration test GREEN**

Run:

```bash
go test ./cmd/rtc-probe -run '^TestMixedOfferProductionAudioPrimeProducesOpusRTP$' -count=1
```

Expected: the mixed-offer loopback receives exactly the two required Opus RTP packets and passes.

---

### Task 5: Full verification and self-review

**Files:**
- Review all files changed by Tasks 1-4.

**Interfaces:**
- Produces verification evidence only.

- [ ] **Step 1: Format Go sources**

```bash
gofmt -w internal/config/config.go internal/config/config_test.go cmd/rtc-server/main.go internal/rtc/server.go internal/rtc/session.go internal/rtc/audio_prime.go internal/rtc/audio_prime_test.go internal/rtc/session_test.go cmd/rtc-probe/main_test.go
```

- [ ] **Step 2: Run all Go tests**

```bash
go test ./...
```

- [ ] **Step 3: Run the race detector**

```bash
go test -race ./...
```

- [ ] **Step 4: Build the production image without cache**

```bash
docker build --no-cache .
```

- [ ] **Step 5: Check patch whitespace and scope**

```bash
git diff --check
```

Confirm that Lambda, SDP normalizers, video handling, ICE, codec registration, recording behavior, Dockerfile, and Compose have no changes.

- [ ] **Step 6: Self-review safety and behavior**

Verify the disabled default, one goroutine per session, post-tick cancellation check, fixed completion reasons, absence of frame/SDP/credential/token logging, and exact mixed-offer RTP assertions.
