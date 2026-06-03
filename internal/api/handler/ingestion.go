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
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "请求体格式错误")
		return
	}

	// Validate request body is an object
	if req.Event == "" && req.Payload == nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "请求体不合法")
		return
	}

	if req.Event == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "event 不能为空")
		return
	}

	if req.Payload == nil {
		req.Payload = map[string]any{}
	}

	// Auto-generate idempotent_key if not provided
	if req.IdempotentKey == "" {
		req.IdempotentKey = randomHex(16)
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
			writeError(w, http.StatusUnprocessableEntity, "EVENT_NOT_FOUND", "事件类型未注册")
			return
		}
		if ingestion.IsSchemaValidationError(err) {
			details := ingestion.GetSchemaValidationDetails(err)
			writeErrorWithDetails(w, http.StatusUnprocessableEntity, "SCHEMA_VALIDATION_FAILED", "payload 校验失败", details)
			return
		}
		log.Error().Err(err).Msg("ingestion service error")
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "服务暂时不可用")
		return
	}

	resp := map[string]any{
		"data": map[string]any{
			"notification_id": notification.ID,
			"status":          notification.Status,
			"created_at":      notification.CreatedAt.Format(time.RFC3339),
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(resp)
}

// GetStatus handles GET /api/v1/notifications/{id}.
func (h *Handler) GetStatus(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "缺少 id")
		return
	}

	notification, err := h.svc.GetByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "通知不存在")
		return
	}

	tasks, err := h.svc.GetDeliveryTasks(r.Context(), notification.ID)
	if err != nil {
		log.Error().Err(err).Str("notification_id", id).Msg("failed to fetch delivery tasks")
	}

	deliveryResults := make([]map[string]any, 0, len(tasks))
	for _, t := range tasks {
		deliveryResults = append(deliveryResults, map[string]any{
			"vendor_id":    t.VendorID,
			"status":       t.Status,
			"retry_count":  t.RetryCount,
			"last_error":   t.LastError,
			"event":        t.EventType,
			"updated_at":   t.UpdatedAt.Format(time.RFC3339),
		})
	}

	resp := map[string]any{
		"data": map[string]any{
			"notification_id": notification.ID,
			"caller_id":       notification.CallerID,
			"event":           notification.EventType,
			"status":          notification.Status,
			"payload":         notification.Payload,
			"delivery_results": deliveryResults,
			"created_at":      notification.CreatedAt.Format(time.RFC3339),
			"updated_at":      notification.UpdatedAt.Format(time.RFC3339),
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// List handles GET /api/v1/notifications?caller_id=...&event=...&page=...&page_size=...
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	callerID := r.URL.Query().Get("caller_id")
	event := r.URL.Query().Get("event")

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}

	pageSize, _ := strconv.Atoi(r.URL.Query().Get("page_size"))
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	notifications, total, err := h.svc.List(r.Context(), callerID, event, page, pageSize)
	if err != nil {
		log.Error().Err(err).Msg("list notifications error")
		writeError(w, http.StatusInternalServerError, "SERVICE_UNAVAILABLE", "服务暂时不可用")
		return
	}

	items := make([]map[string]any, 0, len(notifications))
	for _, n := range notifications {
		items = append(items, map[string]any{
			"notification_id": n.ID,
			"caller_id":       n.CallerID,
			"event":           n.EventType,
			"status":          n.Status,
			"created_at":      n.CreatedAt.Format(time.RFC3339),
			"updated_at":      n.UpdatedAt.Format(time.RFC3339),
		})
	}

	totalPages := (total + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}

	resp := map[string]any{
		"data": map[string]any{
			"items":       items,
			"total":       total,
			"page":        page,
			"page_size":   pageSize,
			"total_pages": totalPages,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	body := map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}

// randomHex generates a random hex string of n bytes (2n hex chars).
func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func writeErrorWithDetails(w http.ResponseWriter, status int, code, message string, details any) {
	body := map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
			"details": details,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body)
}
