import assert from 'node:assert/strict';
import test from 'node:test';

import {createHandler} from './index.mjs';

const env = {
  RTC_SERVER_URL: 'http://198.51.100.10:8080',
  RTC_SERVER_TOKEN: 'sentinel-secret-token',
};

function loggerCapture() {
  const entries = [];
  const calls = [];
  return {
    entries,
    calls,
    logger: {
      info(message, metadata) {
        calls.push({level: 'info', message, metadata});
        entries.push(JSON.stringify([message, metadata]));
      },
      error(message, metadata) {
        calls.push({level: 'error', message, metadata});
        entries.push(JSON.stringify([message, metadata]));
      },
    },
  };
}

function directive(namespace, name, payload = {}, options = {}) {
  return {
    directive: {
      header: {
        namespace,
        name,
        messageId: 'request-message',
        correlationToken: 'correlation-1',
        payloadVersion: '3',
      },
      endpoint: {
        scope: {type: 'BearerToken', token: 'alexa-oauth-sentinel'},
        endpointId: 'home-brain-001',
      },
      payload,
      ...options,
    },
  };
}

function handlerWith(fetchImpl, extra = {}) {
  return createHandler({
    fetchImpl,
    logger: {info() {}, error() {}},
    env,
    randomUUID: () => 'response-message',
    ...extra,
  });
}

test('Discover returns the Home Brain full-duplex RTC endpoint', async () => {
  const handler = handlerWith(async () => { throw new Error('fetch must not run'); });
  const response = await handler(directive('Alexa.Discovery', 'Discover', {}, {endpoint: undefined}));
  assert.equal(response.event.header.namespace, 'Alexa.Discovery');
  assert.equal(response.event.header.name, 'Discover.Response');
  assert.equal(response.event.payload.endpoints.length, 1);
  const endpoint = response.event.payload.endpoints[0];
  assert.equal(endpoint.endpointId, 'home-brain-001');
  assert.equal(endpoint.friendlyName, 'Home Brain');
  assert.deepEqual(endpoint.displayCategories, ['CAMERA']);
  const rtc = endpoint.capabilities.find((capability) => capability.interface === 'Alexa.RTCSessionController');
  assert.equal(rtc.version, '3');
  assert.equal(rtc.configuration.isFullDuplexAudioSupported, true);
  const health = endpoint.capabilities.find((capability) => capability.interface === 'Alexa.EndpointHealth');
  assert.equal(health.properties.proactivelyReported, false);
  assert.equal(health.properties.retrievable, true);
});

test('AcceptGrant returns Alexa.Authorization success', async () => {
  const response = await handlerWith(async () => {})(directive('Alexa.Authorization', 'AcceptGrant', {grant: {type: 'OAuth2.AuthorizationCode', code: 'secret-code'}}));
  assert.equal(response.event.header.namespace, 'Alexa.Authorization');
  assert.equal(response.event.header.name, 'AcceptGrant.Response');
  assert.deepEqual(response.event.payload, {});
});

test('ReportState preserves endpoint and correlation token', async () => {
  const request = directive('Alexa', 'ReportState');
  const response = await handlerWith(async () => {})(request);
  assert.equal(response.event.header.name, 'StateReport');
  assert.equal(response.event.header.correlationToken, 'correlation-1');
  assert.deepEqual(response.event.endpoint, request.directive.endpoint);
  assert.equal(response.context.properties[0].value.value, 'OK');
});

test('InitiateSessionWithOffer sends offer and returns answer with routing metadata', async () => {
  let observed;
  const fetchImpl = async (url, options) => {
    observed = {url, options};
    return new Response(JSON.stringify({sessionId: 'session-1', answerSdp: 'v=0\r\nsentinel-answer'}), {status: 200});
  };
  const request = directive('Alexa.RTCSessionController', 'InitiateSessionWithOffer', {
    sessionId: 'session-1',
    offer: {format: 'SDP', value: 'v=0\r\nsentinel-offer-sdp'},
  });
  const response = await handlerWith(fetchImpl)(request);
  assert.equal(observed.url, `${env.RTC_SERVER_URL}/v1/rtc/sessions`);
  assert.equal(observed.options.method, 'POST');
  assert.equal(observed.options.headers.authorization, `Bearer ${env.RTC_SERVER_TOKEN}`);
  assert.deepEqual(JSON.parse(observed.options.body), {sessionId: 'session-1', offerSdp: 'v=0\r\nsentinel-offer-sdp'});
  assert.equal(response.event.header.name, 'AnswerGeneratedForSession');
  assert.equal(response.event.header.correlationToken, 'correlation-1');
  assert.deepEqual(response.event.endpoint, request.directive.endpoint);
  assert.deepEqual(response.event.payload.answer, {format: 'SDP', value: 'v=0\r\nsentinel-answer'});
});

test('InitiateSessionWithOffer logs safe answer-return metadata', async () => {
  const capture = loggerCapture();
  const request = directive('Alexa.RTCSessionController', 'InitiateSessionWithOffer', {
    sessionId: 'session-1',
    offer: {format: 'SDP', value: 'v=0\r\nsentinel-offer-sdp'},
  });
  const response = await handlerWith(
    async () => new Response(JSON.stringify({sessionId: 'session-1', answerSdp: 'v=0\r\nsentinel-answer'}), {status: 200}),
    {logger: capture.logger},
  )(request, {
    functionVersion: '42',
    invokedFunctionArn: 'arn:aws:lambda:eu-west-1:123456789012:function:homebrain:42',
  });

  const diagnostics = capture.calls.filter((entry) => entry.message === 'rtc_answer_returned');
  assert.equal(diagnostics.length, 1);
  const {elapsedMs, ...metadata} = diagnostics[0].metadata;
  assert.deepEqual(metadata, {
    functionVersion: '42',
    invokedFunctionArn: 'arn:aws:lambda:eu-west-1:123456789012:function:homebrain:42',
    requestNamespace: 'Alexa.RTCSessionController',
    requestName: 'InitiateSessionWithOffer',
    responseNamespace: 'Alexa.RTCSessionController',
    responseName: 'AnswerGeneratedForSession',
    payloadVersion: '3',
    hasRequestCorrelationToken: true,
    hasResponseCorrelationToken: true,
    correlationTokenMatches: true,
    hasRequestEndpointId: true,
    hasResponseEndpointId: true,
    endpointIdMatches: true,
    hasScope: true,
    answerFormat: 'SDP',
    answerType: 'string',
    answerBytes: 20,
    answerStartsWithV0: true,
    hasStatusCodeWrapper: false,
    hasBodyWrapper: false,
  });
  assert.equal(Number.isFinite(elapsedMs), true);
  assert.equal(elapsedMs >= 0, true);
  assert.deepEqual(response.event.payload.answer, {format: 'SDP', value: 'v=0\r\nsentinel-answer'});
  assert.equal(Object.hasOwn(response, 'statusCode'), false);
  assert.equal(Object.hasOwn(response, 'body'), false);
});

test('InitiateSessionWithOffer logs missing routing identifiers separately from equality', async () => {
  const capture = loggerCapture();
  const request = directive('Alexa.RTCSessionController', 'InitiateSessionWithOffer', {
    sessionId: 'session-without-routing',
    offer: {format: 'SDP', value: 'v=0\r\noffer'},
  });
  delete request.directive.header.correlationToken;
  delete request.directive.endpoint.endpointId;

  await handlerWith(
    async () => new Response(JSON.stringify({answerSdp: 'v=0\r\nanswer'}), {status: 200}),
    {logger: capture.logger},
  )(request, {});

  const metadata = capture.calls.find((entry) => entry.message === 'rtc_answer_returned')?.metadata;
  assert.equal(metadata.hasRequestCorrelationToken, false);
  assert.equal(metadata.hasResponseCorrelationToken, false);
  assert.equal(metadata.correlationTokenMatches, true);
  assert.equal(metadata.hasRequestEndpointId, false);
  assert.equal(metadata.hasResponseEndpointId, false);
  assert.equal(metadata.endpointIdMatches, true);
});

test('RTC server timeout returns ENDPOINT_UNREACHABLE', async () => {
  const fetchImpl = (_url, {signal}) => new Promise((_resolve, reject) => {
    signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), {once: true});
  });
  const response = await handlerWith(fetchImpl, {timeoutMs: 10})(directive('Alexa.RTCSessionController', 'InitiateSessionWithOffer', {
    sessionId: 'slow', offer: {format: 'SDP', value: 'v=0\r\noffer'},
  }));
  assert.equal(response.event.header.namespace, 'Alexa');
  assert.equal(response.event.header.name, 'ErrorResponse');
  assert.equal(response.event.payload.type, 'ENDPOINT_UNREACHABLE');
});

test('RTC server 500 returns ENDPOINT_UNREACHABLE', async () => {
  const response = await handlerWith(async () => new Response('{"error":"failed"}', {status: 500}))(
    directive('Alexa.RTCSessionController', 'InitiateSessionWithOffer', {
      sessionId: 'failed', offer: {format: 'SDP', value: 'v=0\r\noffer'},
    }),
  );
  assert.equal(response.event.payload.type, 'ENDPOINT_UNREACHABLE');
});

test('SessionConnected posts observation and returns matching event', async () => {
  let observed;
  const response = await handlerWith(async (url, options) => {
    observed = {url, options};
    return new Response('{"status":"ok"}', {status: 200});
  })(directive('Alexa.RTCSessionController', 'SessionConnected', {sessionId: 'session-1'}));
  assert.equal(observed.url, `${env.RTC_SERVER_URL}/v1/rtc/sessions/session-1/connected`);
  assert.equal(observed.options.method, 'POST');
  assert.equal(response.event.header.name, 'SessionConnected');
  assert.equal(response.event.payload.sessionId, 'session-1');
  assert.equal(response.event.header.correlationToken, 'correlation-1');
});

test('SessionDisconnected deletes session and returns matching event', async () => {
  let observed;
  const response = await handlerWith(async (url, options) => {
    observed = {url, options};
    return new Response('{"status":"closed"}', {status: 200});
  })(directive('Alexa.RTCSessionController', 'SessionDisconnected', {sessionId: 'session-1'}));
  assert.equal(observed.url, `${env.RTC_SERVER_URL}/v1/rtc/sessions/session-1`);
  assert.equal(observed.options.method, 'DELETE');
  assert.equal(response.event.header.name, 'SessionDisconnected');
  assert.equal(response.event.payload.sessionId, 'session-1');
  assert.equal(response.event.header.correlationToken, 'correlation-1');
});

test('logs never contain tokens or SDP', async () => {
  const capture = loggerCapture();
  const handler = createHandler({
    fetchImpl: async () => new Response(JSON.stringify({sessionId: 'session-1', answerSdp: 'v=0\r\nsentinel-answer-sdp'}), {status: 200}),
    logger: capture.logger,
    env,
    randomUUID: () => 'response-message',
  });
  await handler(directive('Alexa.RTCSessionController', 'InitiateSessionWithOffer', {
    sessionId: 'session-1', offer: {format: 'SDP', value: 'v=0\r\nsentinel-offer-sdp'},
  }));
  const logs = capture.entries.join('\n');
  for (const secret of [
    env.RTC_SERVER_TOKEN,
    'alexa-oauth-sentinel',
    'correlation-1',
    'sentinel-offer-sdp',
    'sentinel-answer-sdp',
  ]) {
    assert.equal(logs.includes(secret), false, `logs contained ${secret}`);
  }
  assert.match(logs, /InitiateSessionWithOffer/);
  assert.match(logs, /session-1/);
});
