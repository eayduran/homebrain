# Alexa RTCP-Mux Candidate Interoperability Normalization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove component-2 ICE candidates only from accepted RTCP-mux audio sections in completed Alexa answers while preserving every other byte.

**Architecture:** Add a second Alexa-specific response-boundary normalizer after the existing rejected-video normalizer. It filters whole SDP lines without parsing/remarshalling the document, preserves the detected line delimiter and terminal-newline state, and uses the candidate grammar's second whitespace-separated field for exact component matching.

**Tech Stack:** Go 1.26, Pion WebRTC v4, Docker Compose v2, Docker

## Global Constraints

- This is Alexa interoperability normalization, not generic SDP behavior.
- Remove only exact component `2` candidate lines from accepted `m=audio` sections containing exact `a=rtcp-mux`.
- Preserve every component-1 candidate byte-for-byte and in order.
- Preserve `\r\n` versus `\n` and final-newline presence/absence.
- Do not change ICE credentials, candidate fields, DTLS, MID/BUNDLE, Opus/audio direction, video normalization, Lambda, networking, recording, lifecycle, or ICE-Lite defaults/options.
- Do not commit or push; Git operations belong to the user.

---

### Task 1: Focused RED Tests

**Files:**
- Modify: `internal/rtc/session_test.go`

**Interfaces:**
- Produces test coverage for: `normalizeAlexaRTCPMuxCandidates(answer string) string`

- [ ] **Step 1: Add a deterministic real-Pion answer fixture**

Create a test-only Pion API with UDP4, mDNS disabled, loopback candidates enabled, and an IP filter accepting only IPv4 loopback. Register Opus, apply a real offer, add a local Opus track, create/set the answer, wait for gathering completion, and return the raw local SDP. Assert the muxed audio section contains exactly one component-1 and one component-2 candidate before normalization.

- [ ] **Step 2: Write the real-Pion RED assertion**

Apply `normalizeAlexaRTCPMuxCandidates` to the raw Pion answer. Require exactly one component-1 candidate, zero component-2 candidates, an unchanged component-1 line, and full output equality with the raw answer after deleting only the exact component-2 line and its delimiter.

- [ ] **Step 3: Add the multi-candidate byte-preservation unit table**

Create CRLF-with-final-newline and LF-without-final-newline cases. Each fixture contains an accepted muxed audio section with multiple interleaved component-1 and component-2 candidates, exact-token nonmatches such as components `12` and `20`, malformed component-2-looking lines, a non-muxed audio section, a rejected muxed audio section, and another media section containing component-2. Expected output removes only eligible component-2 lines. Assert component-1 lines remain byte-identical and ordered, malformed lines and other sections are unchanged, line delimiter is unchanged, and terminal-newline state is unchanged.

- [ ] **Step 4: Run focused tests for RED**

```bash
go test ./internal/rtc -run 'TestAlexaRTCPMuxCandidateNormalization' -count=1 -v
```

If the helper is initially undefined, add only a no-op helper returning `answer` and call it after existing video normalization. Re-run and require assertion failure because component-2 candidates remain.

---

### Task 2: Minimal Production Normalizer

**Files:**
- Modify: `internal/rtc/server.go`
- Test: `internal/rtc/session_test.go`

**Interfaces:**
- Consumes: completed, video-normalized SDP answer
- Produces: `normalizeAlexaRTCPMuxCandidates(answer string) string`

- [ ] **Step 1: Preserve input framing**

Detect `\r\n` when present, otherwise use `\n`. Split and rejoin using that exact delimiter; rely on the trailing empty split element to preserve final-newline state. Return empty or media-less SDP unchanged.

- [ ] **Step 2: Identify eligible sections**

Find each `m=` section boundary. An eligible section must have an `m=audio` m-line with a successfully parsed numeric port greater than zero and contain an exact `a=rtcp-mux` line.

- [ ] **Step 3: Filter exact candidate component tokens**

Within eligible sections only, inspect lines beginning `a=candidate:`. Parse with `strings.Fields`; require the second field to equal `"2"` exactly. Validate a whitespace-canonicalized copy with Pion's ICE candidate parser so malformed lines remain untouched, but never marshal or substitute the original line. Remove only valid component-2 candidate lines and append every retained line without modification.

- [ ] **Step 4: Chain after rejected-video normalization**

At the completed-answer boundary:

```go
videoNormalized := normalizeAlexaRejectedVideoPayloads(offer, local.SDP)
normalizedAnswer := normalizeAlexaRTCPMuxCandidates(videoNormalized)
```

Keep existing safe byte-count logging and return the final answer. Do not log SDP or candidate values.

- [ ] **Step 5: Run focused tests for GREEN**

```bash
go test ./internal/rtc -run 'TestAlexaRTCPMuxCandidateNormalization' -count=1 -v
```

Expected: all focused tests pass.

---

### Task 3: Regression and Full Verification

**Files:**
- Review: `internal/rtc/server.go`
- Review: `internal/rtc/session_test.go`

**Interfaces:**
- Produces verification evidence only

- [ ] **Step 1: Format and check the diff**

```bash
gofmt -w internal/rtc/server.go internal/rtc/session_test.go
git diff --check
```

- [ ] **Step 2: Run all Go tests and race tests**

```bash
go test ./...
go test -race ./...
```

- [ ] **Step 3: Validate Compose without creating `.env`**

Render an in-memory copy substituting `.env.example` for `.env`, run `docker compose config`, and confirm success while leaving the repository secrets-free.

- [ ] **Step 4: Build the production image**

```bash
docker build --no-cache .
```

- [ ] **Step 5: Review scope and request independent review**

Confirm only `internal/rtc/server.go`, focused RTC tests, and design/plan documents changed. Verify existing ICE-Lite config remains opt-in/default false and no forbidden subsystem changed. Resolve all actionable review findings, rerun affected tests, and report the uncommitted verified worktree.
