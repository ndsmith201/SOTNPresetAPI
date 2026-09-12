package catalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

type Request struct {
	Method, Path, ContentType, Subject, Username string
	Query                                        map[string]string
	Body                                         []byte
}

type Response struct {
	Status  int
	Headers map[string]string
	Body    string
}

type API struct{ Store Store }

func reply(status int, value any) Response {
	switch item := value.(type) {
	case Item:
		value = canonicalOptionItem(item)
	case Page:
		items := make([]Item, len(item.Items))
		for i, entry := range item.Items {
			items[i] = canonicalOptionItem(entry)
		}
		item.Items = items
		value = item
	}

	b, err := json.Marshal(value)
	if err != nil {
		status = 500
		b = []byte(`{"error":{"code":"internal_error","message":"internal server error"}}`)
	}
	return Response{Status: status, Headers: map[string]string{"Content-Type": "application/json", "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff"}, Body: string(b)}
}

func failure(status int, code, message string) Response {
	return reply(status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (a API) Handle(ctx context.Context, r Request) Response {
	if r.Path == "/healthz" && r.Method == http.MethodGet {
		return reply(200, map[string]string{"status": "ok"})
	}
	parts := strings.Split(strings.TrimPrefix(r.Path, "/"), "/")
	if len(parts) < 2 || len(parts) > 4 || parts[0] != "v1" || (parts[1] != "options" && parts[1] != "presets") {
		return failure(404, "not_found", "route not found")
	}
	kind := parts[1]
	if len(parts) >= 3 && !ValidID(parts[2]) {
		return failure(404, "not_found", "item not found")
	}
	if len(parts) == 4 && parts[3] != "vote" {
		return failure(404, "not_found", "route not found")
	}
	allowed := "GET"
	if len(parts) == 2 {
		allowed = "GET, POST"
	}
	if len(parts) == 4 {
		allowed = "PUT"
	}
	if !(r.Method == "GET" && len(parts) < 4 || r.Method == "POST" && len(parts) == 2 || r.Method == "PUT" && len(parts) == 4) {
		res := failure(405, "method_not_allowed", "method not allowed")
		res.Headers["Allow"] = allowed
		return res
	}
	if r.Method != "GET" {
		if r.Subject == "" {
			return failure(401, "unauthorized", "authentication required")
		}
		if len(r.Body) > MaxBodyBytes {
			return failure(413, "body_too_large", "body exceeds 128 KiB")
		}
		media, _, err := mime.ParseMediaType(r.ContentType)
		if err != nil || media != "application/json" {
			return failure(415, "unsupported_media_type", "Content-Type must be application/json")
		}
	}
	if r.Method == "POST" {
		item, err := NewItem(kind, r.Subject, r.Body)
		if err != nil {
			return failure(400, "invalid_request", err.Error())
		}
		if kind == "presets" {
			return a.sharePreset(ctx, item, r.Username)
		}
		return a.shareOption(ctx, item)
	}
	if r.Method == "PUT" {
		var body struct {
			Value *int `json:"value"`
		}
		if err := DecodeStrict(r.Body, &body); err != nil || body.Value == nil || *body.Value < -1 || *body.Value > 1 {
			return failure(400, "invalid_request", "value must be -1, 0, or 1")
		}
		item, err := a.Store.Vote(ctx, kind, parts[2], r.Subject, *body.Value)
		if err != nil {
			return storeError(err)
		}
		return reply(200, item)
	}
	if len(parts) == 3 {
		item, err := a.Store.Get(ctx, kind, parts[2])
		if err != nil {
			return storeError(err)
		}
		return reply(200, item)
	}
	limit := 20
	if raw, ok := r.Query["limit"]; ok {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 50 {
			return failure(400, "invalid_request", "limit must be between 1 and 50")
		}
		limit = n
	}
	after := ""
	if raw := r.Query["cursor"]; raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		prefix := kind + ":"
		if err != nil || !strings.HasPrefix(string(decoded), prefix) || !ValidID(strings.TrimPrefix(string(decoded), prefix)) {
			return failure(400, "invalid_request", "invalid cursor")
		}
		after = strings.TrimPrefix(string(decoded), prefix)
	}
	page, err := a.Store.List(ctx, kind, limit, after)
	if err != nil {
		return storeError(err)
	}
	if page.Items == nil {
		page.Items = []Item{}
	}
	if page.NextCursor != "" {
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(kind + ":" + page.NextCursor))
	}
	return reply(200, page)
}

func storeError(err error) Response {
	switch {
	case errors.Is(err, ErrNotFound):
		return failure(404, "not_found", err.Error())
	case errors.Is(err, ErrConflict):
		return failure(409, "conflict", err.Error())
	case errors.Is(err, ErrForbidden):
		return failure(403, "forbidden", err.Error())
	default:
		slog.Error("database operation failed", "error", err)
		return failure(500, "internal_error", "internal server error")
	}
}
