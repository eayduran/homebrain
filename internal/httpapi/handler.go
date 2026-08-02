package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strings"

	"home-brain-rtc/internal/rtc"
)

const maxRequestBody = 1 << 20

var sessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type SessionService interface {
	Create(context.Context, string, string) (string, error)
	MarkConnected(string) error
	Close(string) error
}

type handler struct {
	tokenHash [sha256.Size]byte
	sessions  SessionService
	logger    *slog.Logger
	mux       *http.ServeMux
}

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func New(token string, sessions SessionService, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &handler{tokenHash: sha256.Sum256([]byte(token)), sessions: sessions, logger: logger, mux: http.NewServeMux()}
	h.mux.HandleFunc("/healthz", h.health)
	h.mux.HandleFunc("/v1/rtc/sessions", h.createSession)
	h.mux.HandleFunc("/v1/rtc/sessions/{sessionId}/connected", h.connected)
	h.mux.HandleFunc("/v1/rtc/sessions/{sessionId}", h.session)
	h.mux.HandleFunc("/v1/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "resource not found")
	})
	return h
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v1/") && !h.authorized(r.Header.Get("Authorization")) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "a valid bearer token is required")
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *handler) authorized(header string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	presented := sha256.Sum256([]byte(strings.TrimPrefix(header, prefix)))
	return subtle.ConstantTimeCompare(presented[:], h.tokenHash[:]) == 1
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

type createSessionRequest struct {
	SessionID string `json:"sessionId"`
	OfferSDP  string `json:"offerSdp"`
}

func (h *handler) createSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
		return
	}
	var request createSessionRequest
	if status, err := decodeJSON(w, r, &request); err != nil {
		code := "invalid_request"
		message := "request body must be one valid JSON object"
		if status == http.StatusRequestEntityTooLarge {
			code, message = "request_too_large", "request body must not exceed 1 MiB"
		}
		writeError(w, status, code, message)
		return
	}
	if !sessionIDPattern.MatchString(request.SessionID) {
		writeError(w, http.StatusBadRequest, "invalid_session_id", "sessionId must contain only safe ASCII identifier characters")
		return
	}
	if strings.TrimSpace(request.OfferSDP) == "" {
		writeError(w, http.StatusBadRequest, "invalid_sdp", "offerSdp must not be empty")
		return
	}
	answer, err := h.sessions.Create(r.Context(), request.SessionID, request.OfferSDP)
	if err != nil {
		h.writeRTCError(w, request.SessionID, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"sessionId": request.SessionID, "answerSdp": answer})
}

func (h *handler) connected(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	id := r.PathValue("sessionId")
	if !sessionIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid_session_id", "sessionId must contain only safe ASCII identifier characters")
		return
	}
	if err := h.sessions.MarkConnected(id); err != nil {
		h.writeRTCError(w, id, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) session(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	id := r.PathValue("sessionId")
	if !sessionIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "invalid_session_id", "sessionId must contain only safe ASCII identifier characters")
		return
	}
	if err := h.sessions.Close(id); err != nil {
		h.writeRTCError(w, id, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "closed"})
}

func (h *handler) writeRTCError(w http.ResponseWriter, sessionID string, err error) {
	status, code, message := http.StatusInternalServerError, "internal_error", "internal server error"
	switch {
	case errors.Is(err, rtc.ErrInvalidSDP):
		status, code, message = http.StatusBadRequest, "invalid_sdp", "offer SDP is invalid"
	case errors.Is(err, rtc.ErrOpusRequired):
		status, code, message = http.StatusBadRequest, "opus_required", "offer SDP must contain an Opus audio codec"
	case errors.Is(err, rtc.ErrSessionExists):
		status, code, message = http.StatusConflict, "session_exists", "sessionId already exists"
	case errors.Is(err, rtc.ErrSessionNotFound):
		status, code, message = http.StatusNotFound, "session_not_found", "session was not found"
	case errors.Is(err, rtc.ErrAnswerTimeout):
		status, code, message = http.StatusGatewayTimeout, "answer_timeout", "SDP answer generation timed out"
	}
	if status == http.StatusInternalServerError {
		h.logger.Error("session_error", "sessionId", sessionID, "category", code)
	}
	writeError(w, status, code, message)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) (int, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return http.StatusRequestEntityTooLarge, err
		}
		return http.StatusBadRequest, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return http.StatusRequestEntityTooLarge, err
		}
		return http.StatusBadRequest, errors.New("trailing JSON value")
	}
	return http.StatusOK, nil
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	response := apiError{}
	response.Error.Code = code
	response.Error.Message = message
	writeJSON(w, status, response)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
