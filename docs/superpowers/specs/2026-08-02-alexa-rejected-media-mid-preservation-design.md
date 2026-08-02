# Alexa Rejected-Media MID Preservation Design

## Scope

Add one answer-boundary Alexa interoperability normalization that restores a missing media-level `a=mid:` attribute on rejected SDP media sections. The purpose is only to reject Alexa-offered media in standards-compliant offer/answer form; it does not add video codecs, transceivers, tracks, RTP handling, or any active video path.

## Positional matching

Collect offer and answer `m=` sections in source order. Answer section index N may use only offer section index N. Do not reorder or search by media type. If the offer section is absent, either m-line is malformed, or their media types differ, leave the answer section unchanged.

An answer section is eligible only when its m-line port token is exactly `0`. If any line in that answer section begins with `a=mid:`, it is considered present even when empty or different from the offer; never rewrite or duplicate it. If the corresponding offer section has no non-empty `a=mid:` value, make no change.

## Insertion and byte preservation

For an eligible section, insert `a=mid:` plus the exact suffix from the offer MID line immediately before the first media-level line beginning `a=`. This places MID after media-level connection/bandwidth/key fields while changing only one inserted line. If the section has no media-level attribute, append MID at the section end.

Preserve CRLF versus LF and final-newline state. Do not alter session-level fields, including `a=group:BUNDLE`; therefore the current `a=group:BUNDLE 0` remains byte-identical. A rejected MID already present in a bundle-only group is likewise left unchanged because this helper never edits BUNDLE.

## Pipeline

Run the helper after Alexa rejected-video payload/inactive normalization and before Alexa RTCP-mux candidate normalization:

1. Pion completed answer;
2. rejected-video payload/inactive normalization;
3. Alexa rejected-media MID preservation;
4. RTCP-mux candidate normalization;
5. logging and response.

## Tests

Use TDD with literal expected SDP. Cover the actual mixed audio/video defect, unchanged `a=group:BUNDLE 0`, unchanged accepted audio MID, full output equality differing only by inserted MID, existing empty/different MID no-op, missing offer section no-op, positional media-type mismatch no-op, several ordered media sections, and CRLF/LF plus final-newline preservation. Existing video payload and RTCP-mux candidate tests remain regression coverage.
