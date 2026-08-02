import {randomUUID as nodeRandomUUID} from 'node:crypto';

const DEFAULT_TIMEOUT_MS = 4800;

export function createHandler({
  fetchImpl = globalThis.fetch,
  logger = console,
  env = process.env,
  randomUUID = nodeRandomUUID,
  timeoutMs = DEFAULT_TIMEOUT_MS,
} = {}) {
  const responseHeader = (namespace, name, requestHeader, includeCorrelation = true) => {
    const header = {
      namespace,
      name,
      messageId: randomUUID(),
      payloadVersion: '3',
    };
    if (includeCorrelation && requestHeader?.correlationToken) {
      header.correlationToken = requestHeader.correlationToken;
    }
    return header;
  };

  const rtcRequest = async (path, {
    method = 'POST',
    body,
    acceptedStatuses = [],
    returnStatus = false,
  } = {}) => {
    if (!env.RTC_SERVER_URL || !env.RTC_SERVER_TOKEN) {
      throw new Error('RTC server environment is incomplete');
    }
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeoutMs);
    try {
      const response = await fetchImpl(`${env.RTC_SERVER_URL.replace(/\/$/, '')}${path}`, {
        method,
        signal: controller.signal,
        headers: {
          authorization: `Bearer ${env.RTC_SERVER_TOKEN}`,
          'content-type': 'application/json',
        },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      if (!response.ok && !acceptedStatuses.includes(response.status)) {
        throw new Error('RTC server returned a non-success status');
      }
      if (returnStatus) {
        return response.status;
      }
      const text = await response.text();
      return text === '' ? undefined : JSON.parse(text);
    } finally {
      clearTimeout(timer);
    }
  };

  const endpointEvent = (request, name, payload) => ({
    event: {
      header: responseHeader('Alexa.RTCSessionController', name, request.header),
      endpoint: request.endpoint,
      payload,
    },
  });

  const errorResponse = (request) => {
    const event = {
      header: responseHeader('Alexa', 'ErrorResponse', request.header),
      payload: {
        type: 'ENDPOINT_UNREACHABLE',
        message: 'Home Brain RTC gateway is unreachable.',
      },
    };
    if (request.endpoint) {
      event.endpoint = request.endpoint;
    }
    return {event};
  };

  return async function handle(requestEnvelope, context) {
    const startedAt = Date.now();
    const request = requestEnvelope?.directive;
    if (!request?.header?.namespace || !request?.header?.name) {
      throw new Error('invalid Alexa directive');
    }
    const {namespace, name} = request.header;
    const sessionId = request.payload?.sessionId;
    logger.info('directive_received', {
      namespace,
      directiveName: name,
      endpointId: request.endpoint?.endpointId,
      sessionId,
    });

    if (namespace === 'Alexa.Discovery' && name === 'Discover') {
      return {
        event: {
          header: responseHeader('Alexa.Discovery', 'Discover.Response', request.header, false),
          payload: {
            endpoints: [{
              endpointId: 'home-brain-001',
              manufacturerName: 'Home Brain',
              description: 'Home Brain full-duplex audio intercom',
              friendlyName: 'Home Brain',
              displayCategories: ['CAMERA'],
              capabilities: [
                {type: 'AlexaInterface', interface: 'Alexa', version: '3'},
                {
                  type: 'AlexaInterface',
                  interface: 'Alexa.RTCSessionController',
                  version: '3',
                  configuration: {isFullDuplexAudioSupported: true},
                },
                {
                  type: 'AlexaInterface',
                  interface: 'Alexa.EndpointHealth',
                  version: '3',
                  properties: {
                    supported: [{name: 'connectivity'}],
                    proactivelyReported: false,
                    retrievable: true,
                  },
                },
              ],
            }],
          },
        },
      };
    }

    if (namespace === 'Alexa.Authorization' && name === 'AcceptGrant') {
      return {
        event: {
          header: responseHeader('Alexa.Authorization', 'AcceptGrant.Response', request.header, false),
          payload: {},
        },
      };
    }

    if (namespace === 'Alexa' && name === 'ReportState') {
      return {
        context: {
          properties: [{
            namespace: 'Alexa.EndpointHealth',
            name: 'connectivity',
            value: {value: 'OK'},
            timeOfSample: new Date().toISOString(),
            uncertaintyInMilliseconds: 0,
          }],
        },
        event: {
          header: responseHeader('Alexa', 'StateReport', request.header),
          endpoint: request.endpoint,
          payload: {},
        },
      };
    }

    try {
      if (namespace === 'Alexa.RTCSessionController' && name === 'InitiateSessionWithOffer') {
        const offer = request.payload?.offer;
        if (!sessionId || offer?.format !== 'SDP' || typeof offer.value !== 'string' || offer.value === '') {
          throw new Error('invalid RTC offer directive');
        }
        const result = await rtcRequest('/v1/rtc/sessions', {
          body: {sessionId, offerSdp: offer.value},
        });
        if (typeof result?.answerSdp !== 'string' || result.answerSdp === '') {
          throw new Error('RTC server response does not contain answer SDP');
        }
        const response = endpointEvent(request, 'AnswerGeneratedForSession', {
          answer: {format: 'SDP', value: result.answerSdp},
        });
        logger.info('rtc_answer_returned', {
          functionVersion: context?.functionVersion,
          invokedFunctionArn: context?.invokedFunctionArn,

          requestNamespace: request.header?.namespace,
          requestName: request.header?.name,

          responseNamespace: response.event?.header?.namespace,
          responseName: response.event?.header?.name,
          payloadVersion: response.event?.header?.payloadVersion,

          hasRequestCorrelationToken: Boolean(request.header?.correlationToken),
          hasResponseCorrelationToken: Boolean(response.event?.header?.correlationToken),
          correlationTokenMatches:
            response.event?.header?.correlationToken ===
            request.header?.correlationToken,

          hasRequestEndpointId: Boolean(request.endpoint?.endpointId),
          hasResponseEndpointId: Boolean(response.event?.endpoint?.endpointId),
          endpointIdMatches:
            response.event?.endpoint?.endpointId ===
            request.endpoint?.endpointId,

          hasScope: Boolean(response.event?.endpoint?.scope),
          answerFormat: response.event?.payload?.answer?.format,
          answerType: typeof response.event?.payload?.answer?.value,
          answerBytes: Buffer.byteLength(
            response.event?.payload?.answer?.value ?? '',
            'utf8',
          ),
          answerStartsWithV0:
            response.event?.payload?.answer?.value?.startsWith('v=0') === true,

          hasStatusCodeWrapper:
            Object.prototype.hasOwnProperty.call(response, 'statusCode'),
          hasBodyWrapper:
            Object.prototype.hasOwnProperty.call(response, 'body'),

          elapsedMs: Date.now() - startedAt,
        });
        return response;
      }

      if (namespace === 'Alexa.RTCSessionController' && name === 'SessionConnected') {
        if (!sessionId) {
          throw new Error('sessionId is required');
        }
        await rtcRequest(`/v1/rtc/sessions/${encodeURIComponent(sessionId)}/connected`);
        return endpointEvent(request, 'SessionConnected', {sessionId});
      }

      if (namespace === 'Alexa.RTCSessionController' && name === 'SessionDisconnected') {
        if (!sessionId) {
          throw new Error('sessionId is required');
        }
        const status = await rtcRequest(`/v1/rtc/sessions/${encodeURIComponent(sessionId)}`, {
          method: 'DELETE',
          acceptedStatuses: [404, 410],
          returnStatus: true,
        });
        logger.info('rtc_session_cleanup_completed', {
          status: status === 404 || status === 410 ? 'already_absent' : 'deleted',
        });
        return endpointEvent(request, 'SessionDisconnected', {sessionId});
      }
    } catch {
      logger.error('rtc_request_failed', {
        namespace,
        directiveName: name,
        endpointId: request.endpoint?.endpointId,
        sessionId,
        category: 'endpoint_unreachable',
      });
      return errorResponse(request);
    }

    throw new Error(`unsupported directive: ${namespace}.${name}`);
  };
}

const defaultHandler = createHandler();

export async function handler(event, context) {
  return defaultHandler(event, context);
}
