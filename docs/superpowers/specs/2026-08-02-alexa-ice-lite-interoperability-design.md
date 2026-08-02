# Alexa ICE-Lite Interoperability Experiment Design

**Date:** 2026-08-02

## Scope

Add an opt-in Alexa interoperability experiment that enables Pion ICE-Lite behavior only when `ICE_LITE=true`. The default remains strictly `false`. This is not a general networking-mode change and must not alter existing behavior unless explicitly enabled.

## Configuration and Wiring

Add `ICELite bool` to the typed Go configuration and to `rtc.Options`. Parse `ICE_LITE` as a boolean: an unset or empty value resolves to `false`; valid explicit boolean values are accepted; invalid values fail startup with a configuration error.

Pass `Config.ICELite` through `cmd/rtc-server` into `rtc.Options.ICELite`. In `rtc.NewServer`, call `settings.SetLite(true)` only when the option is enabled. Do not call it in normal mode.

Add `ICE_LITE=false` to `.env.example` and explicitly map `ICE_LITE: "${ICE_LITE:-false}"` in the `home-brain-rtc` service environment. Validate the rendered Compose model with `ICE_LITE=true` and assert that the application container receives string value `true`; also validate the unset/default rendering as `false`. Merely documenting the variable in `.env.example` is not sufficient evidence.

## Preserved Behavior

Do not change codec registration, local or remote audio track behavior, Alexa rejected-video normalization, ICE address rewrite rules, UDP port range configuration, Lambda, recording, or session lifecycle. Continue complete non-trickle ICE gathering; neither normal nor ICE-Lite answers may advertise `a=ice-options:trickle`.

## Tests

Use TDD and real Pion peers.

Configuration tests prove:

- unset `ICE_LITE` defaults to `false`;
- `ICE_LITE=true` parses to `true`;
- invalid values are rejected.

RTC answer tests prove:

- ICE-Lite mode includes `a=ice-lite`;
- normal mode omits `a=ice-lite`;
- neither mode advertises `a=ice-options:trickle`;
- ICE-Lite mode retains the rewritten public candidate, accepted Opus audio, and rejected/inactive video normalization with offered payload `102`.

The integration test creates a full local Pion offerer and an ICE-Lite answerer using loopback-reachable candidates. It applies the returned answer, waits until both PeerConnections reach `connected` or ICE `connected/completed`, and then closes both sides. The test waits for both peers to reach closed terminal state and fails on timeout, proving clean shutdown rather than relying only on cleanup registration.

## Operations and Documentation

Document `ICE_LITE` as an Alexa compatibility experiment, disabled by default. Deployment must explicitly set `ICE_LITE=true` in `.env`; normal deployments retain existing full-ICE behavior.

Run formatting, focused RED/GREEN tests, all Go tests, `go test -race ./...`, Compose rendering validation, and Docker build. Do not commit or push; Git operations belong to the user.
