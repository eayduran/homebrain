# Lambda RTC Answer Diagnostic Log Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add safe scalar diagnostics immediately before the successful Alexa RTC answer is returned.

**Architecture:** Capture invocation start time inside the per-invocation handler, build the successful Alexa response once, log derived scalar metadata, and return the same object. Forward Lambda context through the exported handler without changing any other path.

**Tech Stack:** Node.js 24, ECMAScript modules, `node:test`, AWS Lambda

## Global Constraints

- Change only `lambda/index.mjs`, `lambda/index.test.mjs`, and these design/plan documents.
- Do not change response behavior, SDP, VPS code, networking, RTC configuration, or error behavior.
- Never log whole request, response, answer, or error objects, nor SDP/token/ICE/candidate/fingerprint values.
- Construct the successful response once and return the identical object after logging.
- Capture `startedAt` at the beginning of every handler invocation.
- Do not commit or push; Git operations belong to the user.

---

### Task 1: RED diagnostic metadata test

**Files:**
- Modify: `lambda/index.test.mjs`
- Test: `lambda/index.test.mjs`

**Interfaces:**
- Consumes: `createHandler(options)` returning `(requestEnvelope, context) => Promise<object>`
- Produces: regression coverage for the `rtc_answer_returned` log

- [ ] Add a focused `InitiateSessionWithOffer logs safe answer-return metadata` test using a captured logger and Lambda context.
- [ ] Assert request/response namespaces and names, payload version, all four presence booleans, both equality booleans, scope presence, answer format/type/UTF-8 bytes/`v=0`, wrapper booleans, context values, and non-negative elapsed milliseconds.
- [ ] Assert exactly one `rtc_answer_returned` entry and that the handler response remains the expected unwrapped Alexa event.
- [ ] Strengthen the sensitive-log test to reject the SDP content sentinel, correlation-token value, endpoint scope token, and RTC authorization token.
- [ ] Run `npm test` in `lambda/` and observe failure because `rtc_answer_returned` is absent.

### Task 2: Minimal success-path logging

**Files:**
- Modify: `lambda/index.mjs`
- Test: `lambda/index.test.mjs`

**Interfaces:**
- Per-invocation handler: `async function handle(requestEnvelope, context)`
- Exported handler: `async function handler(event, context)`

- [ ] Capture `const startedAt = Date.now()` as the first statement in `handle`.
- [ ] In only the successful `InitiateSessionWithOffer` path, assign `endpointEvent(...)` once to `response`.
- [ ] Call `logger.info('rtc_answer_returned', metadata)` with only the approved scalar fields, including separate presence booleans before the equality booleans.
- [ ] Return the same `response` reference immediately after logging.
- [ ] Forward `context` from the exported handler to `defaultHandler`.
- [ ] Run `npm test` in `lambda/` and require all tests to pass.

### Task 3: Verification and review

**Files:**
- Review: `lambda/index.mjs`
- Review: `lambda/index.test.mjs`

**Interfaces:**
- Produces verification evidence only

- [ ] Run `npm test` in `lambda/` again as the final Lambda suite.
- [ ] Run `git diff --check`.
- [ ] Inspect the diff for forbidden object/value logging and unrelated changes.
- [ ] Request independent code review, address Critical/Important findings, and rerun affected verification.
