# Alexa Rejected Video Payload Interoperability Normalization

**Date:** 2026-08-02

## Scope

Add one isolated Alexa interoperability normalization after Pion has generated and fully gathered the local SDP answer. This is not generic SDP rewriting and does not add video support.

For a mixed Alexa offer with supported Opus audio and one or more video media sections, inspect each corresponding rejected answer video section. Normalize a section only when its answer m-line has port `0` and its payload list is either empty or contains only Pion's synthetic payload `0`. Replace that payload list with payload `102` when `102` was offered in the corresponding video m-line; otherwise use the first payload type from that offered video m-line. Ensure the rejected answer video section contains `a=inactive`.

## Preservation Requirements

The normalization must preserve byte-for-byte:

- the complete audio media section, including the audio m-line and Opus negotiation;
- MID values and BUNDLE ordering;
- ICE candidates, ports, credentials, and fingerprints;
- DTLS setup;
- every unrelated SDP line and its ordering.

It must not add an active video track or change Lambda, recording, networking, ICE gathering, or audio behavior. SDP and ICE credentials, including ICE ufrag values, must not be logged. VPS-only `sdp_offer_safe`, `sdp_answer_safe`, and `safeSDPLines` diagnostics are explicitly excluded from the repository.

## Implementation Boundary

Implement a narrowly named helper in `internal/rtc/server.go` and invoke it only on the completed local answer immediately before returning it. Use line-preserving manipulation rather than unmarshalling and remarshalling the full answer, so preserved fields remain byte-identical. Match offered and answered video sections by video-section order.

If an offered video section has no usable payload type, or the corresponding answer video section is not rejected or already contains a real payload type, leave that answer section entirely unchanged. Add `a=inactive` only to a rejected section whose synthetic/empty payload list is normalized.

## Test Design

Add a focused real-Pion test producing a mixed offer with Opus payload `111` and video payloads `102` and `112`. Before implementation, the test must fail because Pion returns a rejected video m-line with only synthetic payload `0` rather than retained payload `102`.

After implementation, the test must verify:

- audio remains accepted with Opus;
- rejected video uses port `0`, retains exactly payload `102`, and is inactive;
- no active video track is negotiated;
- the complete audio media section is byte-identical to the pre-normalization Pion answer;
- all candidate lines are byte-identical to the pre-normalization Pion answer;
- MID/BUNDLE, ICE credentials/fingerprint, and DTLS setup are unchanged by construction and focused preservation assertions.

Run the focused RED and GREEN test, all Go tests, `go test -race ./...`, and a production binary build.
