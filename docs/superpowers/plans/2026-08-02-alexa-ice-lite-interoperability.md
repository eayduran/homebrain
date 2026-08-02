# Alexa ICE-Lite Interoperability Experiment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a disabled-by-default `ICE_LITE` option that enables Pion ICE-Lite for an Alexa interoperability experiment and prove real peer connectivity.

**Architecture:** Parse `ICE_LITE` in typed configuration, pass it through `cmd/rtc-server` to `rtc.Options`, and conditionally enable `SettingEngine.SetLite(true)`. Keep all existing negotiation and lifecycle paths intact; verify behavior using real Pion offers and a full local offerer-to-ICE-Lite-answerer connection.

**Tech Stack:** Go 1.26, Pion WebRTC v4, Docker Compose v2, Docker

## Global Constraints

- `ICE_LITE` defaults strictly to `false`.
- ICE-Lite is an opt-in Alexa interoperability experiment, not the normal mode.
- Do not change codec registration, audio tracks, video normalization, ICE address rewriting, UDP port range, Lambda, recording, or session lifecycle.
- Continue complete non-trickle ICE gathering and never advertise `a=ice-options:trickle`.
- Do not commit or push; Git operations belong to the user.

---

### Task 1: Typed Configuration and Wiring

**Files:**
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/config.go`
- Modify: `cmd/rtc-server/main.go`

**Interfaces:**
- Produces: `config.Config.ICELite bool`
- Produces: `rtc.Options.ICELite bool`

- [ ] **Step 1: Write failing configuration tests**

Extend the defaults test to assert `Config.ICELite == false`. Add a test loading `ICE_LITE=true` and expecting `true`. Add `ICE_LITE=enabled` to invalid-configuration cases and require an error.

- [ ] **Step 2: Run configuration tests for RED**

```bash
go test ./internal/config -run 'TestLoad.*ICE|TestLoadAppliesDefaultsAndParsesValues|TestLoadRejectsInvalidConfiguration' -count=1 -v
```

Expected: compile/assertion failure because `Config.ICELite` does not exist.

- [ ] **Step 3: Add the minimum typed parser**

Add `ICELite bool` to `Config`. Use a focused parser that returns `false` for empty input, uses `strconv.ParseBool` for explicit values, and returns `ICE_LITE must be true or false` on invalid input. Populate the field in `Load`.

- [ ] **Step 4: Pass the value into RTC options**

Add `ICELite bool` to `rtc.Options` and set `ICELite: cfg.ICELite` in `cmd/rtc-server/main.go` without changing any other option.

- [ ] **Step 5: Run configuration tests for GREEN**

```bash
go test ./internal/config ./cmd/rtc-server -count=1
```

---

### Task 2: ICE-Lite SDP and Real Peer Integration

**Files:**
- Modify: `internal/rtc/session_test.go`
- Modify: `internal/rtc/server.go`

**Interfaces:**
- Consumes: `rtc.Options.ICELite bool`
- Produces: conditional `settings.SetLite(true)` behavior

- [ ] **Step 1: Add option-aware test server construction**

Extend the RTC test server helper so tests can choose `ICELite` while retaining the existing public-IP, UDP-range, recorder, timeout, logger, and disconnect-grace values unchanged.

- [ ] **Step 2: Write failing SDP behavior tests**

Create a table-driven test for normal and ICE-Lite servers. Assert normal mode omits `a=ice-lite`; ICE-Lite mode includes it; neither answer contains `a=ice-options:trickle`. In ICE-Lite mixed audio/video mode also assert Opus audio, rewritten `8.8.8.8` candidate, rejected video port `0`, payload `102`, and `a=inactive` remain present.

- [ ] **Step 3: Write the failing real-peer integration test**

Create a full local Pion offerer using the existing Opus fixture and an ICE-Lite server with loopback-reachable candidates. Apply the answer and observe both peers. Accept WebRTC `connected` or ICE `connected/completed` as established. Close the `Session` and offerer, then wait until both peer connection states are `closed`, failing on a bounded timeout.

- [ ] **Step 4: Run focused RTC tests for RED**

```bash
go test ./internal/rtc -run 'TestNewSessionICE|TestICELitePeerConnection' -count=1 -v
```

Expected: ICE-Lite answer lacks `a=ice-lite`, and the integration requirement fails before `SetLite(true)` is wired.

- [ ] **Step 5: Enable ICE-Lite minimally**

Immediately after existing `SettingEngine` network/mDNS setup and without changing address rewrite or port-range code, add:

```go
if opts.ICELite {
    settings.SetLite(true)
}
```

- [ ] **Step 6: Run focused RTC tests for GREEN**

```bash
go test ./internal/rtc -run 'TestNewSessionICE|TestICELitePeerConnection' -count=1 -v
```

Expected: all focused tests pass and both peers close cleanly.

---

### Task 3: Deployment Wiring and Documentation

**Files:**
- Modify: `.env.example`
- Modify: `docker-compose.yml`
- Modify: `README.md`

**Interfaces:**
- Consumes: `ICE_LITE` environment variable
- Produces: documented and Compose-verified container environment

- [ ] **Step 1: Wire and document the disabled default**

Add `ICE_LITE=false` to `.env.example`. Add `ICE_LITE: "${ICE_LITE:-false}"` to the `home-brain-rtc` service `environment` mapping in `docker-compose.yml`. Add `ICE_LITE` to the README environment table and identify it as an opt-in Alexa interoperability experiment; deployment must explicitly set `ICE_LITE=true`.

- [ ] **Step 2: Verify Compose passes an explicit true value**

Render an in-memory Compose copy that substitutes `.env.example` for the missing local `.env`, set process environment `ICE_LITE=true`, and inspect the rendered `home-brain-rtc.environment.ICE_LITE` value. Require it to equal string `true`.

- [ ] **Step 3: Confirm normal example remains false**

Inspect `.env.example` and require exactly one `ICE_LITE=false` entry. Render the same in-memory Compose copy with `ICE_LITE` unset and require `home-brain-rtc.environment.ICE_LITE` to equal string `false`.

---

### Task 4: Full Verification and Review

**Files:**
- Review all modified files

**Interfaces:**
- Produces verification evidence only

- [ ] **Step 1: Format and inspect**

```bash
gofmt -w internal/config/config.go internal/config/config_test.go cmd/rtc-server/main.go internal/rtc/server.go internal/rtc/session_test.go
git diff --check
git diff
```

- [ ] **Step 2: Run all Go tests**

```bash
go test ./...
```

- [ ] **Step 3: Run race tests**

```bash
go test -race ./...
```

- [ ] **Step 4: Build production artifacts**

```bash
CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /tmp/homebrain-rtc-server ./cmd/rtc-server
docker build --no-cache -t homebrain-ice-lite-check .
```

- [ ] **Step 5: Final scope and secret review**

Confirm no Lambda, recording, codec-registration, audio-track, address-rewrite, UDP-range, video-normalization, or lifecycle code changed. Confirm `.env` and generated binaries are absent from Git status. Request independent code review and resolve all actionable findings before reporting completion.
