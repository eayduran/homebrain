# Alexa Rejected-Media MID Preservation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Restore missing offer MIDs on positionally corresponding rejected answer media sections without changing any other SDP bytes.

**Architecture:** Add a separate line-oriented normalizer after rejected-video payload normalization and before RTCP-mux candidate normalization. It matches media sections by index, requires matching media types and answer port zero, and inserts one MID line only when the answer has no MID attribute.

**Tech Stack:** Go 1.26, Pion WebRTC v4, Docker

## Global Constraints

- This is explicitly Alexa rejected-media MID preservation, not generic SDP rewriting.
- Do not reorder media sections or rematch them by type.
- Missing offer section, malformed m-line, or mismatched offer/answer media type is a no-op.
- Existing answer `a=mid:` is always a no-op even when empty or different.
- Preserve `a=group:BUNDLE`, audio, candidates, ICE credentials, fingerprint/setup, codecs, line endings, video payload normalization, RTCP-mux normalization, and final-newline state.
- Do not add video codecs, transceivers, tracks, RTP handling, or active video behavior.
- Do not commit or push; Git operations belong to the user.

---

### Task 1: Focused RED tests

**Files:**
- Modify: `internal/rtc/session_test.go`

**Interfaces:**
- Produces coverage for `normalizeAlexaRejectedMediaMIDs(offer, answer string) string`.

- [ ] Add a CRLF mixed offer/answer fixture where offer sections are audio MID `0` and video MID `1`, answer keeps accepted audio MID `0`, rejects video at port `0`, and omits its MID.
- [ ] Require output equality with a literal answer containing only `a=mid:1` inserted before video `a=inactive`; require `a=group:BUNDLE 0` and audio section byte identity.
- [ ] Add LF/no-final-newline multi-section fixtures proving index matching for audio/video/application, insertion on multiple rejected matching sections, and preserved section order.
- [ ] Add no-op cases for an existing correct MID, existing different MID, existing empty MID, absent offer section, malformed m-line, and positional media-type mismatch.
- [ ] Run `go test ./internal/rtc -run TestAlexaRejectedMediaMIDPreservation -count=1 -v` and observe undefined-helper failure.

### Task 2: Minimal normalizer

**Files:**
- Modify: `internal/rtc/server.go`
- Test: `internal/rtc/session_test.go`

**Interfaces:**
- Consumes offer and video-normalized answer SDP strings.
- Produces answer SDP with only missing rejected-media MID lines inserted.

- [ ] Detect the answer line separator and split/join without losing terminal empty elements.
- [ ] Collect ordered offer sections and their media type/MID using offer's own CRLF-or-LF separator.
- [ ] Iterate answer sections by index; require a corresponding offer section, matching exact media type token, answer port field exactly `0`, no existing `a=mid:` prefix, and a non-empty offer MID suffix.
- [ ] Insert the exact offer MID line immediately before the first media-level `a=` line, or at section end when no attribute exists.
- [ ] Run focused tests and require PASS.

### Task 3: Pipeline integration and regression

**Files:**
- Modify: `internal/rtc/server.go`
- Test: `internal/rtc/session_test.go`

**Interfaces:**
- Chains `normalizeAlexaRejectedMediaMIDs(offer, videoNormalizedAnswer)` before `normalizeAlexaRTCPMuxCandidates`.

- [ ] Add the normalizer to the completed-answer boundary without changing logging, PeerConnection setup, or media behavior.
- [ ] Extend the real mixed-offer session test to require rejected video `a=mid:1`, unchanged BUNDLE `0`, accepted audio MID `0`, and no active video sender.
- [ ] Run all focused Alexa normalization tests.

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
- [ ] Run `git diff --check` and verify no Lambda file changed.
- [ ] Request independent review, resolve Critical/Important findings, and rerun affected verification.
