# Go ICE SDP Diagnostic Metadata Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extend `sdp_answer_generated` with safe typed ICE-Lite, trickle, and BUNDLE MID diagnostics.

**Architecture:** Add a read-only, line-oriented session-level SDP metadata extractor in the RTC package. Apply it to the received offer and final normalized answer immediately before the existing log call, adding only booleans and string arrays to that event.

**Tech Stack:** Go 1.26, `log/slog`, Pion WebRTC v4, Docker

## Global Constraints

- Preserve the `sdp_answer_generated` event name and `sessionId`, `offerBytes`, and `answerBytes` fields.
- Do not change SDP, ICE behavior/roles/candidates, codecs, networking, configuration, Lambda, or response behavior.
- Detect exact session-level `a=ice-lite`, exact `trickle` tokens in `a=ice-options:`, and MIDs only from the session-level `a=group:BUNDLE` line.
- Return safe `false` and non-nil empty `[]string` values for missing or malformed metadata; extraction must not fail a request.
- Never log raw SDP, SDP lines, candidates, ICE credentials, fingerprints, tokens, authorization data, or parser errors.
- Do not commit or push; Git operations belong to the user.

---

### Task 1: Focused extractor RED tests

**Files:**
- Modify: `internal/rtc/session_test.go`
- Test: `internal/rtc/session_test.go`

**Interfaces:**
- Produces: `extractICESDPLogMetadata(raw string) iceSDPLogMetadata`
- Expected type: `iceSDPLogMetadata{HasIceLite bool, HasTrickle bool, BundleMIDs []string}`

- [ ] Add a CRLF case with exact session-level `a=ice-lite`, `a=ice-options:renomination trickle`, ordered `a=group:BUNDLE audio video data`, and misleading media-level attributes; expect `true`, `true`, and `[]string{"audio", "video", "data"}`.
- [ ] Add an LF case containing `a=ice-lite-extra`, `a=ice-options:nottrickle trickle2`, malformed/empty BUNDLE data, and media-level exact attributes; expect `false`, `false`, and a non-nil empty `[]string{}`.
- [ ] Run `go test ./internal/rtc -run TestExtractICESDPLogMetadata -count=1 -v` and observe an undefined-helper compile failure.

### Task 2: Minimal safe extractor

**Files:**
- Modify: `internal/rtc/server.go`
- Test: `internal/rtc/session_test.go`

**Interfaces:**
- Consumes: arbitrary raw SDP string
- Produces: non-error `iceSDPLogMetadata` with non-nil `BundleMIDs`

- [ ] Define the private metadata struct and initialize `BundleMIDs` with `[]string{}`.
- [ ] Normalize only for reading by converting CRLF to LF and scan lines until the first `m=` line.
- [ ] Match exact `a=ice-lite`; parse exact `a=ice-options:` lines with `strings.Fields` and exact token equality; parse only the first exact `a=group:BUNDLE` attribute and copy MID fields in source order.
- [ ] Ignore malformed/unrecognized lines without returning errors or changing input.
- [ ] Run the focused extractor test and require PASS.

### Task 3: Real log metadata RED/GREEN

**Files:**
- Modify: `internal/rtc/session_test.go`
- Modify: `internal/rtc/server.go`

**Interfaces:**
- Extends existing `sdp_answer_generated` `slog` record with six typed attributes.

- [ ] Add a real `NewSession` test using a JSON `slog` handler, an Alexa-style Pion offer augmented with session-level ICE-Lite/trickle metadata, and an ICE-Lite server.
- [ ] Parse the captured `sdp_answer_generated` JSON record and assert existing fields plus the six new boolean/array values and BUNDLE ordering.
- [ ] Assert serialized logs contain no `a=ice-lite`, `a=ice-options:`, `a=group:BUNDLE`, `a=candidate:`, `a=ice-ufrag:`, `a=ice-pwd:`, or `a=fingerprint:` content.
- [ ] Run the focused log test and observe failure because the six attributes are absent.
- [ ] Extract offer and final normalized-answer metadata immediately before the existing log call, append the six fields, and keep the event name and existing fields unchanged.
- [ ] Rerun focused tests and require PASS.

### Task 4: Full verification

**Files:**
- Review: `internal/rtc/server.go`
- Review: `internal/rtc/session_test.go`

**Interfaces:**
- Produces verification evidence only.

- [ ] Run `gofmt -w internal/rtc/server.go internal/rtc/session_test.go`.
- [ ] Run `go test ./...`.
- [ ] Run `go test -race ./...`.
- [ ] Run `docker build --no-cache .`.
- [ ] Run `git diff --check` and verify Lambda files are absent from `git diff --name-only`.
- [ ] Request independent code review, resolve Critical/Important findings, and rerun affected verification.
