package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"home-brain-rtc/internal/rtc"
)

type fakeSessions struct {
	answer       string
	createErr    error
	connectedErr error
	closeErr     error
}

func (f *fakeSessions) Create(context.Context, string, string) (string, error) {
	return f.answer, f.createErr
}
func (f *fakeSessions) MarkConnected(string) error { return f.connectedErr }
func (f *fakeSessions) Close(string) error         { return f.closeErr }

func testHandler(sessions *fakeSessions) http.Handler {
	return New("test-token", sessions, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func request(t *testing.T, handler http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func TestHealthIsPublic(t *testing.T) {
	response := request(t, testHandler(&fakeSessions{}), http.MethodGet, "/healthz", "", "")
	if response.Code != http.StatusOK || response.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("status/body = %d %q", response.Code, response.Body.String())
	}
}

func TestV1RequiresExactBearerToken(t *testing.T) {
	for _, token := range []string{"", "Bearer wrong", "Basic test-token", "bearer test-token", "Bearer test-token extra"} {
		response := request(t, testHandler(&fakeSessions{}), http.MethodPost, "/v1/rtc/sessions", `{}`, token)
		if response.Code != http.StatusUnauthorized {
			t.Errorf("token %q: status=%d", token, response.Code)
		}
		if !strings.Contains(response.Body.String(), `"code":"unauthorized"`) {
			t.Errorf("token %q: body=%s", token, response.Body.String())
		}
	}
}

func TestCreateSessionReturnsAnswer(t *testing.T) {
	response := request(t, testHandler(&fakeSessions{answer: "v=0\r\nanswer"}), http.MethodPost, "/v1/rtc/sessions",
		`{"sessionId":"session-1","offerSdp":"v=0\\r\\noffer"}`, "Bearer test-token")
	if response.Code != http.StatusOK {
		t.Fatalf("status/body = %d %s", response.Code, response.Body.String())
	}
	var body struct {
		SessionID string `json:"sessionId"`
		AnswerSDP string `json:"answerSdp"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.SessionID != "session-1" || body.AnswerSDP != "v=0\r\nanswer" {
		t.Fatalf("unexpected response: %#v", body)
	}
}

func TestCreateSessionValidatesRequest(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"malformed JSON", `{`, http.StatusBadRequest},
		{"trailing JSON", `{"sessionId":"s","offerSdp":"v=0"} {}`, http.StatusBadRequest},
		{"unknown field", `{"sessionId":"s","offerSdp":"v=0","token":"leak"}`, http.StatusBadRequest},
		{"missing session ID", `{"offerSdp":"v=0"}`, http.StatusBadRequest},
		{"unsafe session ID", `{"sessionId":"../../escape","offerSdp":"v=0"}`, http.StatusBadRequest},
		{"missing offer", `{"sessionId":"s"}`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := request(t, testHandler(&fakeSessions{}), http.MethodPost, "/v1/rtc/sessions", tt.body, "Bearer test-token")
			if response.Code != tt.want {
				t.Fatalf("status/body = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestCreateSessionLimitsBodyToOneMiB(t *testing.T) {
	body := `{"sessionId":"s","offerSdp":"` + strings.Repeat("x", (1<<20)+1) + `"}`
	response := request(t, testHandler(&fakeSessions{}), http.MethodPost, "/v1/rtc/sessions", body, "Bearer test-token")
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status/body = %d %s", response.Code, response.Body.String())
	}
}

func TestCreateSessionRequiresJSONContentTypeWhenProvided(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/rtc/sessions", strings.NewReader(`{"sessionId":"s","offerSdp":"v=0"}`))
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "text/plain")
	response := httptest.NewRecorder()
	testHandler(&fakeSessions{}).ServeHTTP(response, req)
	if response.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status/body = %d %s", response.Code, response.Body.String())
	}
}

func TestCreateSessionMapsRTCErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
		code string
	}{
		{"invalid SDP", rtc.ErrInvalidSDP, http.StatusBadRequest, "invalid_sdp"},
		{"Opus required", rtc.ErrOpusRequired, http.StatusBadRequest, "opus_required"},
		{"duplicate", rtc.ErrSessionExists, http.StatusConflict, "session_exists"},
		{"timeout", rtc.ErrAnswerTimeout, http.StatusGatewayTimeout, "answer_timeout"},
		{"internal", errors.New("boom"), http.StatusInternalServerError, "internal_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := request(t, testHandler(&fakeSessions{createErr: tt.err}), http.MethodPost, "/v1/rtc/sessions",
				`{"sessionId":"s","offerSdp":"v=0"}`, "Bearer test-token")
			if response.Code != tt.want || !strings.Contains(response.Body.String(), `"code":"`+tt.code+`"`) {
				t.Fatalf("status/body = %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestConnectedAndDeleteRoutes(t *testing.T) {
	handler := testHandler(&fakeSessions{})
	connected := request(t, handler, http.MethodPost, "/v1/rtc/sessions/session-1/connected", "", "Bearer test-token")
	if connected.Code != http.StatusOK || connected.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("connected status/body = %d %s", connected.Code, connected.Body.String())
	}
	closed := request(t, handler, http.MethodDelete, "/v1/rtc/sessions/session-1", "", "Bearer test-token")
	if closed.Code != http.StatusOK || closed.Body.String() != "{\"status\":\"closed\"}\n" {
		t.Fatalf("delete status/body = %d %s", closed.Code, closed.Body.String())
	}
}

func TestConnectedAndDeleteReturnNotFound(t *testing.T) {
	handler := testHandler(&fakeSessions{connectedErr: rtc.ErrSessionNotFound, closeErr: rtc.ErrSessionNotFound})
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/rtc/sessions/missing/connected"},
		{http.MethodDelete, "/v1/rtc/sessions/missing"},
	} {
		response := request(t, handler, tc.method, tc.path, "", "Bearer test-token")
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"code":"session_not_found"`) {
			t.Fatalf("%s %s: %d %s", tc.method, tc.path, response.Code, response.Body.String())
		}
	}
}
