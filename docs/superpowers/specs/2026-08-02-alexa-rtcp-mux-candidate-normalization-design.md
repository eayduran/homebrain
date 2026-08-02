# Alexa RTCP-Mux Candidate Interoperability Normalization Design

**Date:** 2026-08-02

## Scope

Add one narrowly scoped Alexa interoperability normalization to completed Pion SDP answers. For an accepted audio media section containing exact attribute `a=rtcp-mux`, remove only ICE candidate lines whose candidate component field is exactly `2`.

This is not generic SDP normalization and does not alter candidate gathering or networking behavior.

## Pipeline

Apply normalization at the response boundary after complete ICE gathering:

1. Pion completed local answer;
2. existing Alexa rejected-video payload normalization;
3. new Alexa RTCP-mux candidate normalization;
4. returned SDP answer.

The new helper is named to make the interoperability boundary explicit, for example `normalizeAlexaRTCPMuxCandidates(answer string) string`.

## Eligibility and Candidate Parsing

Process media sections independently. A section is eligible only when:

- its m-line is `m=audio`;
- its media port is non-zero;
- it contains exact line `a=rtcp-mux`.

For candidate lines beginning with `a=candidate:`, parse the candidate grammar using whitespace-separated fields. The foundation is part of the first field (`a=candidate:<foundation>`), and the component ID is the second field. Remove the line only when that second field is exactly `2`. Do not use substring matching. Preserve malformed candidate lines and candidates with any other component value.

## Byte Preservation

Use line-oriented filtering without SDP unmarshalling/remarshalling. Detect and retain the input line-ending convention (`\r\n` or `\n`) and retain whether the original SDP ended with a final newline. Removing a component-2 line removes that line and its delimiter only. Every remaining byte, including all component-1 candidate lines, spacing, capitalization, ordering, extensions, and terminal-newline state, must remain unchanged.

Do not change sections without `a=rtcp-mux`, rejected audio sections, video sections, or non-audio sections.

## Preserved Behavior

Do not change ICE credentials, candidate IP/port/priority/type/extensions, DTLS fingerprint/setup, MID/BUNDLE, Opus negotiation, audio direction, rejected-video normalization, Lambda, ICE address rewrite, UDP port range, recording, or session lifecycle. ICE-Lite remains opt-in and defaults to `false`.

## TDD

First create a deterministic test-only real Pion answerer that includes loopback UDP4 candidates and yields exactly one component-1 and one component-2 candidate in a muxed accepted audio section. The RED test confirms the raw answer contains both, applies the new helper, and expects exactly one unchanged component-1 candidate, no component-2 candidate, and a full output equal to the raw answer with only the component-2 line removed.

Add a focused multi-candidate unit fixture containing multiple component-1 and component-2 lines in one muxed accepted audio section plus candidates in other sections. Verify all component-1 lines remain in original order and byte-for-byte unchanged, all eligible component-2 lines are removed, other sections are byte-identical, both CRLF and LF are preserved, and final-newline presence/absence is preserved.

Negative cases prove answers remain byte-identical when `a=rtcp-mux` is absent or the audio m-line is rejected with port `0`.

Run `gofmt`, all Go tests, race tests, Compose validation, no-cache Docker build, and `git diff --check`. Do not commit or push; Git operations belong to the user.
