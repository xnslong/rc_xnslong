package handler

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"github.com/xnslong/rc_xnslong/internal/ingestion"
	"github.com/xnslong/rc_xnslong/internal/model"
)

const defaultCallerID = "system"

// Error codes returned in API error responses.
const (
	errCodeInvalidRequest         = "INVALID_REQUEST"
	errCodeEventNotFound          = "EVENT_NOT_FOUND"
	errCodeSchemaValidationFailed = "SCHEMA_VALIDATION_FAILED"
	errCodeServiceUnavailable     = "SERVICE_UNAVAILABLE"
	errCodeNotFound               = "NOT_FOUND"
)

// Chinese error messages for API responses.
const (
	msgInvalidRequestBody  = "请求体格式错误"
	msgInvalidPayload      = "请求体不合法"
	msgEventRequired       = "event 不能为空"
	msgEventNotFound       = "事件类型未注册"
	msgSchemaFailed        = "payload 校验失败"
	msgServiceUnavail      = "服务暂时不可用"
	msgMissingID           = "缺少 id"
	msgNotificationMissing = "通知不存在"
)

// API response JSON field keys.
const (
	respFieldData             = "data"
	respFieldNotificationID   = "notification_id"
	respFieldCallerID         = "caller_id"
	respFieldEvent            = "event"
	respFieldStatus           = "status"
	respFieldPayload          = "payload"
	respFieldDeliveryResults  = "delivery_results"
	respFieldCreatedAt        = "created_at"
	respFieldUpdatedAt        = "updated_at"
	respFieldItems            = "items"
	respFieldTotal            = "total"
	respFieldPage             = "page"
	respFieldPageSize         = "page_size"
	respFieldTotalPages       = "total_pages"
	respFieldVendorID         = "vendor_id"
	respFieldRetryCount       = "retry_count"
	respFieldLastError        = "last_error"
)

// API error response JSON field keys.
const (
	errFieldError   = "error"
	errFieldCode    = "code"
	errFieldMessage = "message"
	errFieldDetails = "details"
)

const (
	defaultPage        = 1
	defaultPageSize    = 20
	maxPageSize        = 100
	idempotentKeyBytes = 16
)

const (
	headerContentType = "Content-Type"
	contentTypeJSON   = "application/json"
)

// Handler handles HTTP requests for notification ingestion.
type Handler struct {
	svc *ingestion.Service
}

// NewHandler creates a new ingestion handler.
func NewHandler(svc *ingestion.Service) *Handler {
	return &Handler{svc: svc}
}

// Ingest handles POST /api/v1/notifications.
func (h *Handler) Ingest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Event         string         `json:"event"`
		IdempotentKey string         `json:"idempotent_key"`
		Payload       map[string]any `json:"payload"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errCodeInvalidRequest, msgInvalidRequestBody)
		return
	}

	if req.Event == "" && req.Payload == nil {
		writeError(w, http.StatusBadRequest, errCodeInvalidRequest, msgInvalidPayload)
		return
	}

	if req.Event == "" {
		writeError(w, http.StatusBadRequest, errCodeInvalidRequest, msgEventRequired)
		return
	}

	if req.Payload == nil {
		req.Payload = map[string]any{}
	}

	if req.IdempotentKey == "" {
		req.IdempotentKey = randomHex(idempotentKeyBytes)
	}

	params := model.UpsertParams{
		CallerID:      defaultCallerID,
		EventType:     req.Event,
		IdempotentKey: req.IdempotentKey,
		Payload:       req.Payload,
	}

	notification, err := h.svc.Submit(r.Context(), params)
	if err != nil {
		if ingestion.IsErrEventNotFound(err) {
			writeError(w, http.StatusUnprocessableEntity, errCodeEventNotFound, msgEventNotFound)
			return
		}
		if ingestion.IsSchemaValidationError(err) {
			details := ingestion.GetSchemaValidationDetails(err)
			writeErrorWithDetails(w, http.StatusUnprocessableEntity, errCodeSchemaValidationFailed, msgSchemaFailed, details)
			return
		}
		log.Error().Err(err).Msg("ingestion service error")
		writeError(w, http.StatusServiceUnavailable, errCodeServiceUnavailable, msgServiceUnavail)
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		respFieldData: map[string]any{
			respFieldNotificationID: notification.ID,
			respFieldStatus:          notification.Status,
			respFieldCreatedAt:      notification.CreatedAt.Format(time.RFC3339),
		},
	})
}

// GetStatus handles GET /api/v1/notifications/{id}.
func (h *Handler) GetStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, errCodeInvalidRequest, msgMissingID)
		return
	}

	notification, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, errCodeNotFound, msgNotificationMissing)
		return
	}

	tasks, err := h.svc.GetDeliveryTasks(r.Context(), notification.ID)
	if err != nil {
		log.Error().Err(err).Str("notification_id", id).Msg("failed to fetch delivery tasks")
	}

	deliveryResults := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		deliveryResults = append(deliveryResults, map[string]any{
			respFieldVendorID:   t.VendorID,
			respFieldStatus:      t.Status,
			respFieldRetryCount: t.RetryCount,
			respFieldLastError:  t.LastError,
			respFieldEvent:       t.EventType,
			respFieldUpdatedAt:  t.UpdatedAt.Format(time.RFC3339),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		respFieldData: map[string]any{
			respFieldNotificationID: notification.ID,
			respFieldCallerID:       notification.CallerID,
			respFieldEvent:           notification.EventType,
			respFieldStatus:          notification.Status,
			respFieldPayload:         notification.Payload,
			respFieldDeliveryResults: deliveryResults,
			respFieldCreatedAt:      notification.CreatedAt.Format(time.RFC3339),
			respFieldUpdatedAt:      notification.UpdatedAt.Format(time.RFC3339),
		},
	})
}

// List handles GET /api/v1/notifications?caller_id=...&event=...&page=...&page_size=...
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	callerID := r.URL.Query().Get("caller_id")
	event := r.URL.Query().Get("event")

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = defaultPage
	}

	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}

	notifications, total, err := h.svc.List(r.Context(), callerID, event, page, pageSize)
	if err != nil {
		log.Error().Err(err).Msg("list notifications error")
		writeError(w, http.StatusInternalServerError, errCodeServiceUnavailable, msgServiceUnavail)
		return
	}

	items := make([]map[string]any, 0, len(notifications))
	for _, n := range notifications {
		items = append(items, map[string]any{
			respFieldNotificationID: n.ID,
			respFieldCallerID:       n.CallerID,
			respFieldEvent:           n.EventType,
			respFieldStatus:          n.Status,
			respFieldCreatedAt:      n.CreatedAt.Format(time.RFC3339),
			respFieldUpdatedAt:      n.UpdatedAt.Format(time.RFC3339),
		})
	}

	totalPages := (total + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = defaultPage
	}

	writeJSON(w, http.StatusOK, map[string]any{
		respFieldData: map[string]any{
			respFieldItems:     items,
			respFieldTotal:     total,
			respFieldPage:      page,
			respFieldPageSize:  pageSize,
			respFieldTotalPages: totalPages,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set(headerContentType, contentTypeJSON)
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		errFieldError: map[string]any{
			errFieldCode:    code,
			errFieldMessage: message,
		},
	})
}

// randomHex generates a random hex string of n bytes (2n hex chars).
func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func writeErrorWithDetails(w http.ResponseWriter, status int, code, message string, details any) {
	writeJSON(w, status, map[string]any{
		errFieldError: map[string]any{
			errFieldCode:    code,
			errFieldMessage: message,
			errFieldDetails: details,
		},
	})
}
