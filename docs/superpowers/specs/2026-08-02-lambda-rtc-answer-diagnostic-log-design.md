# Lambda RTC Answer Diagnostic Log Design

## Scope

Add one success-path diagnostic log immediately before returning `AnswerGeneratedForSession` from AWS Lambda's `InitiateSessionWithOffer` path. Do not change the Alexa response, SDP, VPS code, RTC configuration, networking, or error behavior.

## Invocation and response flow

The per-invocation handler accepts `(requestEnvelope, context)` and captures `startedAt` as its first operation. The exported Lambda handler accepts `(event, context)` and forwards both arguments.

After the RTC server returns a valid answer, construct the Alexa response exactly once. Log scalar metadata derived from the request, the same response object, Lambda context, and elapsed time; then return that same object without cloning, stringifying, wrapping, or rebuilding it.

## Logged metadata

Log event name `rtc_answer_returned` with only:

- Lambda version and invoked ARN;
- request/response namespace and name plus response payload version;
- request/response correlation-token presence booleans and equality boolean;
- request/response endpoint-ID presence booleans and equality boolean;
- response endpoint scope presence;
- answer format, JavaScript type, UTF-8 byte length, and whether it starts with `v=0`;
- response `statusCode`/`body` wrapper presence booleans;
- elapsed milliseconds.

Never pass the request, response, answer, error, SDP, token values, ICE credentials, candidates, or fingerprints to the logger. Equality booleans may be true when values match, but the separate presence booleans expose the both-undefined case.

## Tests

Add a focused success-path test with Lambda context and a captured logger. Assert every scalar field, both presence and equality booleans, absence of wrappers, non-negative elapsed time, unchanged response content, and exactly one diagnostic event. Preserve and strengthen the existing sensitive-log test so logs contain none of the answer SDP beyond the permitted `answerStartsWithV0` boolean, correlation-token value, endpoint scope token, or RTC authorization token.
