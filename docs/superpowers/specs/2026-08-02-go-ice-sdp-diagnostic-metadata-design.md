# Go ICE SDP Diagnostic Metadata Design

## Scope

Extend the existing `sdp_answer_generated` Go server log event with diagnostic-only ICE metadata. Preserve the event name and its existing `sessionId`, `offerBytes`, and `answerBytes` fields. Do not modify SDP, ICE roles or behavior, candidates, codecs, networking, configuration, Lambda, or response behavior.

## Extraction boundary

Extract offer metadata from the Alexa offer received by `Server.NewSession` and answer metadata from the final normalized answer immediately before the existing log call. The extractor is read-only and returns a typed value containing `HasIceLite bool`, `HasTrickle bool`, and a non-nil `BundleMIDs []string`.

Only lines before the first `m=` line are session-level. Within that region:

- `HasIceLite` is true only for an exact `a=ice-lite` line.
- `HasTrickle` is true only when an exact `a=ice-options:` attribute contains the whitespace-delimited token `trickle`.
- `BundleMIDs` comes only from the first exact `a=group:BUNDLE` attribute, preserving its whitespace-delimited MID order.

Both CRLF and LF inputs are supported. Empty, malformed, or unexpected SDP safely produces `false` and a non-nil empty MID list; diagnostic extraction cannot fail the request.

## Logging and safety

Append exactly these fields to `sdp_answer_generated`:

- `offerHasIceLite`, `answerHasIceLite`: booleans;
- `offerHasTrickle`, `answerHasTrickle`: booleans;
- `offerBundleMids`, `answerBundleMids`: string arrays.

Never pass raw SDP, SDP lines, candidates, ICE credentials, fingerprints, tokens, authorization data, or parsing errors to the logger.

## Tests

Add focused CRLF and LF extractor tests covering exact matches, exact trickle token handling, session/media separation, BUNDLE ordering, and safe defaults. Add a real `NewSession` logging test using a captured `slog` record to verify the existing and new typed metadata fields while proving sensitive SDP values are absent from serialized logs.
