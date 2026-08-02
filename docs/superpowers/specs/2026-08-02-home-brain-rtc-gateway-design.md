# Home Brain RTC Gateway Design

**Date:** 2026-08-02  
**Status:** Approved for implementation planning  
**Scope:** Production-quality, narrowly scoped MVP for Alexa full-duplex audio WebRTC ingress and OGG/Opus recording

## 1. Objective

Home Brain RTC Gateway accepts a WebRTC SDP offer delivered by an Alexa Smart Home skill using `Alexa.RTCSessionController`, generates a non-trickle SDP answer with Pion WebRTC, and records the Echo device's remote Opus RTP stream as an OGG/Opus file on a VPS.

The first milestone is complete when:

1. “Alexa, talk to Home Brain” establishes an audio WebRTC session through Alexa → Lambda → VPS signaling.
2. Audio received from the Echo microphone is written to a finalized `.ogg` file under the configured recordings directory.

STT, LLMs, TTS, VAD, keyword detection, audio processing, WAV conversion, UI, databases, Redis, Kubernetes, MediaMTX, TURN, and video media handling are outside scope.

## 2. Technology and Repository Layout

The implementation uses:

- Go with `github.com/pion/webrtc/v4`
- Pion `oggwriter` for direct Opus RTP recording
- The Go standard-library HTTP server
- Node.js 24 Lambda code with built-in `fetch`, `AbortController`, and `node:test`
- Docker and Docker Compose
- No CGO-dependent codec library

The repository will contain:

```text
home-brain-rtc/
├── .github/workflows/ci.yml
├── cmd/rtc-server/main.go
├── docs/superpowers/specs/2026-08-02-home-brain-rtc-gateway-design.md
├── internal/config/config.go
├── internal/config/config_test.go
├── internal/httpapi/handler.go
├── internal/httpapi/handler_test.go
├── internal/recording/recorder.go
├── internal/recording/recorder_test.go
├── internal/rtc/server.go
├── internal/rtc/session.go
├── internal/rtc/session_manager.go
├── internal/rtc/session_test.go
├── lambda/index.mjs
├── lambda/index.test.mjs
├── lambda/package.json
├── recordings/.gitkeep
├── .env.example
├── .gitignore
├── Dockerfile
├── docker-compose.yml
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

The user will manage Git initialization and commits. The delivered files will contain no real secrets or VPS addresses.

## 3. Architecture and Component Boundaries

### 3.1 Configuration

`internal/config` owns environment parsing and startup validation. It returns a typed configuration containing:

- `HTTP_ADDR`, default `:8080`
- `PUBLIC_IP`, required public IPv4
- `UDP_PORT_MIN`, default `40000`
- `UDP_PORT_MAX`, default `40020`
- `SESSION_API_TOKEN`, required and non-empty
- `RECORDINGS_DIR`, default `/data/recordings`
- `SESSION_TTL`, default `10m`
- `LOG_LEVEL`, default `info`

`PUBLIC_IP` must parse as IPv4 and be globally routable. Unspecified, loopback, multicast, link-local, private, carrier-grade NAT, documentation, benchmarking, and other non-public ranges are rejected. The UDP range must be ordered and use valid non-zero ports. The recordings directory must either exist as a directory or be creatable, and the process must be able to create and remove a small probe file in it. Invalid configuration prevents server startup.

### 3.2 RTC Server

`internal/rtc.Server` owns immutable Pion setup and creates PeerConnections. Its `SettingEngine` will:

- use a constrained UDP port range through Pion's UDP mux/network support;
- permit only UDP and IPv4 candidates;
- disable mDNS candidate generation;
- use the current Pion v4 `SetICEAddressRewriteRules` API to rewrite the selected private host candidate address to `PUBLIC_IP`;
- avoid deprecated `SetNAT1To1IPs`;
- register Opus audio capability explicitly;
- retain the Pion defaults needed for BUNDLE and RTCP multiplexing.

Each PeerConnection gets a local static Opus audio track so the answer can negotiate full-duplex audio. The MVP sends no audio on that track. A goroutine continuously reads the local sender's RTCP packets until the session closes.

Before accepting an offer, the service verifies that its audio media section offers Opus. Absence of Opus produces an actionable `400 invalid_sdp` response.

An incoming video m-line does not cause the entire session to fail. Pion answers the unsupported video section as rejected/inactive while continuing the audio negotiation. No local video track is added, incoming video is not recorded, and successful audio-only operation remains the goal. Tests will use a mixed audio/video offer and verify that audio is accepted while video is rejected or inactive in the answer.

The server sets the remote description, creates and sets the answer, and waits on `GatheringCompletePromise`. It returns only after all ICE candidates are embedded in SDP. Trickle ICE is not used. A request-scoped 4.5-second deadline covers answer creation and ICE gathering; timeout returns a consistent JSON error rather than a partial answer.

### 3.3 Session and Session Manager

`Session` owns exactly one:

- Pion PeerConnection;
- local Opus sender and RTCP reader;
- optional remote audio recorder;
- cancellation context;
- idempotent close operation;
- creation and expiry timestamps;
- observational `connected` flag.

`SessionManager` owns a mutex-protected map keyed by the exact session ID. It provides create, mark-connected, close, and close-all operations. Duplicate create requests return `409 session_exists`; they do not replace an active session.

The connected flag is not a prerequisite for media or lifecycle activity. Alexa may omit `SessionConnected`; therefore PeerConnection setup, `OnTrack`, recording, state monitoring, TTL expiration, API deletion, and shutdown operate independently of that directive. `SessionConnected` is an idempotent observational update only.

The manager schedules TTL cleanup from session creation. On expiry it removes and closes the session. API deletion, TTL cleanup, terminal PeerConnection state, and process shutdown converge on the same idempotent close path. Map removal and resource closure are race-safe.

### 3.4 Recording

`internal/recording` wraps Pion `oggwriter` behind a small recorder interface. When the first remote audio track arrives:

1. The codec MIME type is checked case-insensitively for `audio/opus`.
2. The session ID is sanitized to a restricted filename component; path separators, traversal sequences, control characters, and unsafe punctuation cannot affect the directory.
3. A filename `<UTC_TIMESTAMP>_<SANITIZED_SESSION_ID>.ogg` is created inside `RECORDINGS_DIR`.
4. RTP packets are read and passed directly to `oggwriter`; no decoding or conversion occurs.

The timestamp uses UTC with filesystem-safe, collision-resistant precision. The resolved path is checked to remain within the configured recordings directory. Only the first accepted Opus remote track becomes the recording source. Unsupported or extra tracks never replace it.

Recorder closure finalizes the OGG stream and is idempotent. It occurs on explicit DELETE/SessionDisconnected, TTL expiry, process shutdown, and PeerConnection `failed`, `closed`, or terminal `disconnected` handling. `failed` and `closed` trigger immediate closure. `disconnected` starts a five-second cancellation-safe grace timer; returning to a connected state cancels it, while remaining disconnected closes the session when the timer expires.

### 3.5 HTTP API

The service uses `net/http` and exposes:

- Public `GET /healthz` → `200 {"status":"ok"}`
- Authorized `POST /v1/rtc/sessions`
- Authorized `POST /v1/rtc/sessions/{sessionId}/connected`
- Authorized `DELETE /v1/rtc/sessions/{sessionId}`

All `/v1` routes require an exact `Authorization: Bearer <SESSION_API_TOKEN>` match using constant-time token comparison. Missing or wrong credentials return `401`. The request body is limited to 1 MiB. JSON parsing rejects malformed JSON, trailing values, missing fields, and unsupported content where applicable.

Create request:

```json
{
  "sessionId": "uuid",
  "offerSdp": "v=0..."
}
```

Create success:

```json
{
  "sessionId": "uuid",
  "answerSdp": "v=0..."
}
```

Connected success is `{"status":"ok"}`. Delete success is `{"status":"closed"}`. Deleting or marking a missing session returns `404`.

Errors consistently use:

```json
{
  "error": {
    "code": "invalid_sdp",
    "message": "offer SDP must contain an Opus audio codec"
  }
}
```

Status mapping is:

- `400`: malformed JSON, missing session ID or SDP, invalid SDP, no Opus audio
- `401`: missing or invalid bearer token
- `404`: unknown session
- `409`: duplicate session ID
- `413`: body larger than 1 MiB
- `504`: ICE gathering/answer deadline exceeded
- `500`: unexpected internal failure

The HTTP server configures read-header, read, write, and idle timeouts. SIGINT/SIGTERM initiate graceful HTTP shutdown followed by closure of all sessions and recorders.

## 4. Signaling and Media Flows

### 4.1 InitiateSessionWithOffer

1. Alexa sends `Alexa.RTCSessionController.InitiateSessionWithOffer` to Lambda.
2. Lambda extracts `payload.sessionId` and `payload.offer.value`.
3. Lambda posts them to `${RTC_SERVER_URL}/v1/rtc/sessions` with the configured bearer token and a 4.8-second `AbortController` timeout.
4. The Go service creates the session, applies the offer, adds the local Opus track, gathers ICE fully, and returns the SDP answer within its 4.5-second deadline.
5. Lambda emits `AnswerGeneratedForSession`, preserving correlation token, endpoint scope, and endpoint ID.
6. Echo and Pion exchange WebRTC media directly over the published UDP range.

### 4.2 SessionConnected

Lambda posts `/v1/rtc/sessions/{sessionId}/connected` and returns the matching Alexa `SessionConnected` event with preserved routing metadata and `sessionId` payload. The VPS call marks observation only. If Alexa never sends this directive, WebRTC setup, recording, and cleanup remain fully functional.

### 4.3 SessionDisconnected

Lambda deletes `/v1/rtc/sessions/{sessionId}` and returns the matching Alexa `SessionDisconnected` event with preserved routing metadata and `sessionId`. The Go close path finalizes the writer before returning success.

### 4.4 Lambda Failure Mapping

Network failure, timeout, malformed VPS response, and non-2xx VPS status yield an Alexa `ErrorResponse` with namespace `Alexa`, name `ErrorResponse`, and type `ENDPOINT_UNREACHABLE`. The Lambda does not expose internal VPS error details to Alexa.

## 5. Lambda Directive Behavior

`lambda/index.mjs` supports:

- `Alexa.Authorization.AcceptGrant`
- `Alexa.Discovery.Discover`
- `Alexa.ReportState`
- `Alexa.RTCSessionController.InitiateSessionWithOffer`
- `Alexa.RTCSessionController.SessionConnected`
- `Alexa.RTCSessionController.SessionDisconnected`

Discovery preserves one endpoint:

- endpoint ID `home-brain-001`
- friendly name `Home Brain`
- display category `CAMERA`
- `Alexa.RTCSessionController` version 3 with `isFullDuplexAudioSupported: true`
- `Alexa.EndpointHealth`, proactively reported `false`, retrievable `true`
- `Alexa` interface

ReportState returns endpoint health state. AcceptGrant returns the standard successful authorization response without persisting credentials because OAuth storage is outside this MVP.

Lambda reads `RTC_SERVER_URL` and `RTC_SERVER_TOKEN` from environment variables. Network access is isolated in a helper accepting an injectable `fetch` for deterministic tests. Message IDs use `crypto.randomUUID()`.

## 6. Logging and Sensitive Data

The Go service writes one JSON object per log line using structured logging. It emits:

- `session_created`
- `sdp_answer_generated`
- `ice_state_changed`
- `peer_connection_state_changed`
- `remote_track_received`
- `recording_started`
- `recording_stopped`
- `session_closed`
- `session_error`

Safe fields include session ID, state, codec name, duration, SDP byte length, filename basename, and error category. Neither Go nor Lambda logs bearer/OAuth tokens, complete SDP, ICE passwords, or ICE username fragments. Lambda logs only namespace, directive name, endpoint ID, session ID, and safe error category. Tests capture the logger and assert that sentinel token and SDP strings never appear.

## 7. Container and Deployment Design

The Dockerfile uses a current stable Go build stage and a small runtime image. The final process runs as a non-root user, `/data/recordings` is writable, and TCP port 8080 is exposed. The runtime includes only what is required to run the binary and execute the healthcheck.

Compose defines `home-brain-rtc` with:

- `restart: unless-stopped`
- `.env` via `env_file`
- `8080:8080/tcp`
- `40000-40020:40000-40020/udp`
- `./recordings:/data/recordings`
- a `/healthz` healthcheck

This is compatible with Coolify custom Docker Compose deployment and does not require a domain or reverse proxy. Direct plain HTTP is acceptable only for this private MVP signaling path protected by a strong bearer token. A broader or public production deployment requires TLS and additional hardening.

## 8. Testing Strategy

Implementation follows strict red-green-refactor TDD. Each production behavior begins with a focused test that is run and observed failing for the expected reason before minimal implementation is added.

### 8.1 Go Tests

Tests cover:

- valid/default config parsing and every startup validation rule;
- public health endpoint;
- missing and incorrect bearer token responses;
- missing session ID;
- malformed and invalid SDP;
- an audio offer without Opus;
- a valid offer producing an answer with an audio m-line, Opus, ICE candidate, `rtcp-mux`, and BUNDLE;
- a mixed video/audio offer producing a usable Opus audio answer while rejecting or disabling video;
- duplicate session `409` behavior;
- connected marking being optional and idempotent;
- media/recording proceeding without a connected notification;
- DELETE resource cleanup and OGG finalization;
- TTL cleanup;
- concurrent manager access under `go test -race`;
- real remote Opus RTP producing a non-empty `.ogg` beginning with `OggS`.

RTC integration tests use a second local Pion PeerConnection to generate a real Opus offer and send RTP. Tests do not depend on an Echo device, public network, or external service.

### 8.2 Lambda Tests

Node's built-in test runner covers:

- Discover
- AcceptGrant
- ReportState
- InitiateSessionWithOffer success
- VPS timeout
- VPS 500 response
- SessionConnected
- SessionDisconnected
- preservation of correlation token, endpoint scope, endpoint ID, and session ID
- absence of bearer token and SDP in captured logs
- optional absence of a SessionConnected directive having no effect on subsequent disconnect behavior

### 8.3 Verification Commands

The final verification set is:

```bash
gofmt verification
go vet ./...
go test ./...
go test -race ./...
node --test lambda/index.test.mjs
docker build .
docker compose config
```

Only commands actually executed, with their real outcomes, will be reported.

## 9. CI Design

GitHub Actions checks:

1. `gofmt` produces no diff.
2. `go vet ./...` passes.
3. `go test ./...` passes.
4. `go test -race ./...` passes.
5. `node --test lambda/index.test.mjs` passes on Node.js 24.
6. `docker build .` succeeds.

No secrets or live infrastructure are required in CI.

## 10. Documentation Requirements

README will explain:

- architecture and Alexa → Lambda → Pion signaling;
- direct Echo ↔ Pion media flow;
- every environment variable;
- token generation with `openssl rand -hex 32`;
- VPS and cloud-provider firewall rules for TCP 8080 and UDP 40000–40020;
- setup with `.env`, Compose, and `/healthz` verification;
- Lambda environment and deployment of `lambda/index.mjs`;
- Alexa Developer Console WebRTC debugger usage;
- the voice command “Alexa, talk to Home Brain”;
- log and recording inspection;
- playback with `ffplay` or VLC;
- duplicate session `409` behavior;
- the optional nature of `SessionConnected`;
- mixed video/audio offer handling;
- troubleshooting Lambda timeout, missing candidates, blocked UDP, leaked Docker private candidates, wrong public IP, absent Opus tracks, and unfinalized files;
- the private-MVP limitation of plain HTTP.

## 11. Known Risks and Constraints

- Alexa/Echo interoperability cannot be fully proven by local automated tests; final validation requires the developer console and a physical supported Echo device.
- A public host candidate without TURN will fail behind some NAT/firewall topologies. The target VPS must have a real public IPv4 and the UDP range must be reachable.
- Pion API details, especially ICE address rewrite behavior, must be pinned to the selected `webrtc/v4` version and verified against its official API during implementation.
- ICE gathering must fit the Go 4.5-second and Lambda 4.8-second budgets; slow network conditions can still yield `ENDPOINT_UNREACHABLE`.
- A rejected video m-line is expected to allow Alexa's supported two-way audio-only intercom mode, but device/firmware differences require real-device confirmation.
- Treating prolonged `disconnected` as terminal must balance prompt OGG finalization against transient ICE outages; the implementation uses a tested five-second grace period and does not depend on `SessionConnected`.
- Mounted-volume ownership differs across Docker hosts. Setup and troubleshooting documentation must explain write-permission checks without making the container run as root.

## 12. Acceptance Criteria

The repository is ready for handoff when:

- all specified files exist and contain no real IP address, token, or OAuth credential;
- invalid startup configuration prevents launch;
- all `/v1` routes enforce bearer authentication and consistent errors;
- a valid Opus offer produces a complete, non-trickle, public-candidate SDP answer;
- mixed video/audio offers retain usable audio while video is rejected or inactive;
- recording begins from `OnTrack` without waiting for `SessionConnected`;
- received Opus RTP creates and finalizes an OGG file with an `OggS` header;
- explicit disconnect, TTL, terminal connection state, and graceful shutdown clean up resources;
- Lambda implements all required directives and failure mapping;
- Go, race, Lambda, formatting, vet, Docker build, and Compose validation results are reported truthfully;
- README contains the complete deployment, operation, security, and troubleshooting guide.
