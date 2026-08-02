# Alexa Rejected Video Payload Interoperability Normalization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Normalize Pion's rejected Alexa video m-line from an empty/synthetic payload list to one offered video payload while preserving audio and transport SDP exactly.

**Architecture:** Keep Pion negotiation and ICE gathering unchanged. After gathering completes, pass the offer and completed answer through a narrowly named, line-preserving Alexa interoperability helper that edits only eligible rejected video sections before returning the answer.

**Tech Stack:** Go 1.26, Pion WebRTC v4, Go standard library testing

## Global Constraints

- This is Alexa interoperability normalization, not generic SDP behavior.
- Normalize only rejected video m-lines with port `0` and no payloads or only payload `0`.
- Prefer offered video payload `102`; otherwise retain the first offered video payload.
- Preserve audio, MID/BUNDLE, ICE, fingerprint, DTLS setup, Lambda, recording, and networking behavior.
- Do not log SDP, ICE credentials, or ICE ufrag values.
- Do not add an active video track.
- Do not add VPS-only `sdp_offer_safe`, `sdp_answer_safe`, or `safeSDPLines` diagnostics.
- Do not commit or push; the user owns Git operations.

---

### Task 1: Focused Alexa Normalization Tests

**Files:**
- Modify: `internal/rtc/session_test.go`
- Test: `internal/rtc/session_test.go`

**Interfaces:**
- Consumes: `Server.NewSession(ctx context.Context, id, offer string, onTerminal func()) (*Session, string, error)`
- Produces test coverage for: `normalizeAlexaRejectedVideoPayloads(offer, answer string) string`

- [ ] **Step 1: Extend the mixed-offer fixture with Alexa payloads**

Register video codecs using payload `102` and `112` in the offerer's `MediaEngine`, keep Opus at payload `111`, and add no video track to the production answerer. Confirm the generated offer video m-line contains `102 112`.

- [ ] **Step 2: Write the focused public-behavior assertion**

Update the mixed-offer test to require accepted Opus audio plus a rejected video m-line whose port is `0`, whose sole retained payload is `102`, and whose media section contains `a=inactive`.

- [ ] **Step 3: Add line-preservation assertions**

Give the normalization helper a representative Pion answer containing `m=video 0 UDP/TLS/RTP/SAVPF 0`, a complete audio section, BUNDLE/MID, ICE credentials/candidates/fingerprint, and DTLS setup. Capture the audio section and all `a=candidate:` lines before normalization, then assert they are byte-identical afterward. Assert the entire output differs from the input only in the eligible video m-line and its media direction.

- [ ] **Step 4: Run the focused test before behavior implementation**

Run:

```bash
go test ./internal/rtc -run 'Test.*AlexaRejectedVideoPayload|TestNewSessionKeepsAudioWhenOfferContainsVideo' -count=1 -v
```

If the new helper is initially undefined, add only a no-op stub returning `answer` and call it from the completed-answer return path. Re-run and require a behavioral failure showing rejected video payload `0` where `102` is expected. This no-op establishes the test seam without implementing normalization.

---

### Task 2: Minimal Production Normalization

**Files:**
- Modify: `internal/rtc/server.go`
- Test: `internal/rtc/session_test.go`

**Interfaces:**
- Consumes: completed Pion offer and answer SDP strings
- Produces: `normalizeAlexaRejectedVideoPayloads(offer, answer string) string`

- [ ] **Step 1: Extract offered video payload choices without remarshalling SDP**

Split SDP while retaining its original line delimiter. For each offer `m=video` line, record payload `102` when present; otherwise record its first payload field. Keep choices ordered by video-section occurrence.

- [ ] **Step 2: Normalize only eligible rejected answer video sections**

For each answer `m=video` section matched by video occurrence, require port `0` and payload fields that are absent or exactly `0`. Replace only the m-line payload fields with the recorded offered payload. Within that same section, replace any existing `a=sendrecv`, `a=sendonly`, or `a=recvonly` with `a=inactive`, or insert `a=inactive` if no direction exists. Leave ineligible and unmatched sections byte-identical.

- [ ] **Step 3: Apply normalization only at the response boundary**

After `GatheringCompletePromise` resolves and `peer.LocalDescription()` is non-nil, call:

```go
normalizedAnswer := normalizeAlexaRejectedVideoPayloads(offer, local.SDP)
```

Return `normalizedAnswer` and use its length only for the existing safe byte-count log. Do not log either SDP string or extracted values.

- [ ] **Step 4: Run focused tests to verify GREEN**

Run:

```bash
go test ./internal/rtc -run 'Test.*AlexaRejectedVideoPayload|TestNewSessionKeepsAudioWhenOfferContainsVideo' -count=1 -v
```

Expected: all selected tests pass.

---

### Task 3: Full Verification and Review

**Files:**
- Review: `internal/rtc/server.go`
- Review: `internal/rtc/session_test.go`

**Interfaces:**
- Consumes: completed implementation
- Produces: verification evidence only

- [ ] **Step 1: Format and inspect the diff**

Run:

```bash
gofmt -w internal/rtc/server.go internal/rtc/session_test.go
git diff --check
git diff -- internal/rtc/server.go internal/rtc/session_test.go
```

- [ ] **Step 2: Run all Go tests**

```bash
go test ./...
```

- [ ] **Step 3: Run all Go race tests**

```bash
go test -race ./...
```

- [ ] **Step 4: Build the production binary**

```bash
go build ./cmd/rtc-server
```

- [ ] **Step 5: Confirm forbidden diagnostics and out-of-scope changes are absent**

```bash
rg -n 'sdp_offer_safe|sdp_answer_safe|safeSDPLines|ice-ufrag|ICEUfrag' internal cmd lambda || true
git status --short
```

Review the final diff to confirm only the Alexa answer boundary, focused tests, and design/plan documents changed.
