package catalog

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	_ "golang.org/x/image/webp"
)

const MaxFeaturedModImageBytes = 2 * 1024 * 1024
const MaxFeaturedModPatchBytes = 4 * 1024 * 1024
const MaxFeaturedModBodyBytes = MaxFeaturedModPatchBytes + 64*1024
const FeaturedModPublisherGroup = "featured-mod-publishers"

// Go's time parser also accepts a few non-RFC3339 forms. Keep the published
// input contract strict before validating the calendar date and UTC range.
var featuredModReleaseTimePattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

type FeaturedMod struct {
	ID                string `json:"id" dynamodbav:"id"`
	Title             string `json:"title" dynamodbav:"title"`
	Description       string `json:"description" dynamodbav:"description"`
	Image             string `json:"image" dynamodbav:"image"`
	Patch             string `json:"-" dynamodbav:"patch"`
	DownloadURL       string `json:"downloadUrl" dynamodbav:"-"`
	ReleaseTime       string `json:"releaseTime" dynamodbav:"releaseTime"`
	DownloadAvailable bool   `json:"downloadAvailable" dynamodbav:"-"`
	CreatedBy         string `json:"createdBy" dynamodbav:"createdBy"`
	CreatedAt         string `json:"createdAt" dynamodbav:"createdAt"`
}

type FeaturedModImage struct {
	Data        []byte
	ContentType string
}

// Image storage is kept separate from catalog entries and their voting flows.
type FeaturedModStore interface {
	PublishFeaturedMod(context.Context, FeaturedMod, FeaturedModImage, []byte) (FeaturedMod, error)
	LatestFeaturedMod(context.Context) (FeaturedMod, error)
	GetFeaturedMod(context.Context, string) (FeaturedMod, error)
	FeaturedModDownloadURL(context.Context, FeaturedMod) (string, error)
}

func (a API) handleFeaturedMods(ctx context.Context, r Request, now time.Time) Response {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		res := failure(405, "method_not_allowed", "method not allowed")
		res.Headers["Allow"] = "GET, POST"
		return res
	}
	if r.Method == http.MethodPost {
		if r.Subject == "" {
			return failure(401, "unauthorized", "authentication required")
		}
		if !r.CanPublishFeaturedMods {
			return failure(403, "forbidden", "featured-mod publisher membership required")
		}
		if len(r.Body) > MaxFeaturedModBodyBytes {
			return failure(413, "body_too_large", "multipart body exceeds 4 MiB plus 64 KiB")
		}
		media, params, err := mime.ParseMediaType(r.ContentType)
		if err != nil || media != "multipart/form-data" {
			return failure(415, "unsupported_media_type", "Content-Type must be multipart/form-data")
		}
		mod, upload, patch, err := decodeFeaturedMod(r.Body, params["boundary"])
		if err != nil {
			return failure(400, "invalid_request", err.Error())
		}
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			return storeError(err)
		}
		mod.ID = hex.EncodeToString(id)
		mod.CreatedBy = r.Subject
		mod.CreatedAt = now.UTC().Format(time.RFC3339Nano)
		store, ok := a.Store.(FeaturedModStore)
		if !ok {
			return storeError(errors.New("featured-mod storage is not configured"))
		}
		mod, err = store.PublishFeaturedMod(ctx, mod, upload, patch)
		if err != nil {
			return storeError(err)
		}
		return featuredModReply(201, mod, now)
	}
	store, ok := a.Store.(FeaturedModStore)
	if !ok {
		return storeError(errors.New("featured-mod storage is not configured"))
	}
	mod, err := store.LatestFeaturedMod(ctx)
	if err != nil {
		return storeError(err)
	}
	return featuredModReply(200, mod, now)
}

func featuredModReply(status int, mod FeaturedMod, now time.Time) Response {
	release, err := time.Parse(time.RFC3339Nano, mod.ReleaseTime)
	if err != nil {
		return storeError(errors.New("invalid stored featured-mod release time"))
	}
	// Future releases are visible immediately; only download availability waits.
	mod.DownloadAvailable = !now.Before(release)
	mod.DownloadURL = "/v1/featured-mods/" + mod.ID + "/download"
	res := reply(status, mod)
	if status == 201 {
		res.Headers["Location"] = "/v1/featured-mods"
	}
	return res
}

func (a API) handleFeaturedModDownload(ctx context.Context, r Request, now time.Time) Response {
	parts := strings.Split(strings.TrimPrefix(r.Path, "/"), "/")
	if len(parts) != 4 || !ValidID(parts[2]) || parts[3] != "download" {
		return failure(404, "not_found", "route not found")
	}
	if r.Method != http.MethodGet {
		res := failure(405, "method_not_allowed", "method not allowed")
		res.Headers["Allow"] = "GET"
		return res
	}
	store, ok := a.Store.(FeaturedModStore)
	if !ok {
		return storeError(errors.New("featured-mod storage is not configured"))
	}
	mod, err := store.GetFeaturedMod(ctx, parts[2])
	if err != nil {
		return storeError(err)
	}
	release, err := time.Parse(time.RFC3339Nano, mod.ReleaseTime)
	if err != nil {
		return storeError(errors.New("invalid stored featured-mod release time"))
	}
	if now.Before(release) {
		return failure(403, "not_released", "this mod is not available for download yet")
	}
	url, err := store.FeaturedModDownloadURL(ctx, mod)
	if err != nil {
		return storeError(err)
	}
	return Response{Status: 307, Headers: map[string]string{"Location": url, "Cache-Control": "no-store", "X-Content-Type-Options": "nosniff"}}
}

func decodeFeaturedMod(body []byte, boundary string) (FeaturedMod, FeaturedModImage, []byte, error) {
	var mod FeaturedMod
	var upload FeaturedModImage
	var patch []byte
	if boundary == "" {
		return mod, upload, patch, errors.New("multipart boundary is required")
	}
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	seen := map[string]bool{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return mod, upload, patch, errors.New("invalid multipart body")
		}
		name := part.FormName()
		if seen[name] || (name != "title" && name != "description" && name != "image" && name != "ppf" && name != "releaseTime") {
			return mod, upload, patch, errors.New("use exactly one title, description, image, ppf, and releaseTime")
		}
		seen[name] = true
		limit := 10000
		if name == "title" {
			limit = 200
		}
		if name == "releaseTime" {
			limit = 100
		}
		if name == "image" {
			limit = MaxFeaturedModImageBytes
		}
		if name == "ppf" {
			limit = MaxFeaturedModPatchBytes
		}
		data, err := io.ReadAll(io.LimitReader(part, int64(limit+1)))
		if err != nil || len(data) > limit {
			return mod, upload, patch, errors.New(name + " exceeds its size limit")
		}
		if name == "ppf" {
			if !strings.EqualFold(filepath.Ext(part.FileName()), ".ppf") || !validPPFHeader(data) {
				return mod, upload, patch, errors.New("ppf must be an uploaded .ppf file with a PPF1, PPF2, or PPF3 header")
			}
			patch = data
			continue
		}
		if name == "image" {
			if part.FileName() == "" {
				return mod, upload, patch, errors.New("image must be an uploaded file")
			}
			config, format, err := image.DecodeConfig(bytes.NewReader(data))
			if err != nil || (format != "png" && format != "jpeg" && format != "webp") || config.Width < 1 || config.Height < 1 || config.Width > 4096 || config.Height > 4096 {
				return mod, upload, patch, errors.New("image must be a PNG, JPEG, or WebP at most 4096 by 4096 pixels")
			}
			if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
				return mod, upload, patch, errors.New("invalid image file")
			}
			upload = FeaturedModImage{Data: data, ContentType: "image/" + format}
			continue
		}
		if part.FileName() != "" {
			return mod, upload, patch, errors.New(name + " must be a text field")
		}
		if !utf8.Valid(data) {
			return mod, upload, patch, errors.New(name + " must be valid UTF-8 text")
		}
		value := strings.TrimSpace(string(data))
		switch name {
		case "title":
			mod.Title = value
		case "description":
			mod.Description = value
		case "releaseTime":
			mod.ReleaseTime = value
		}
	}
	if len(seen) != 5 || mod.Title == "" || mod.Description == "" || mod.ReleaseTime == "" || len(upload.Data) == 0 || len(patch) == 0 {
		return mod, upload, patch, errors.New("title, description, image, ppf, and releaseTime are required")
	}
	release, err := time.Parse(time.RFC3339Nano, mod.ReleaseTime)
	if !featuredModReleaseTimePattern.MatchString(mod.ReleaseTime) || err != nil || release.UTC().Year() < 1 || release.UTC().Year() > 9999 {
		return mod, upload, patch, errors.New("releaseTime must be an RFC3339 timestamp with a timezone")
	}
	mod.ReleaseTime = release.UTC().Format(time.RFC3339Nano)
	return mod, upload, patch, nil
}

// Check the version header; patch application remains the client's responsibility.
func validPPFHeader(data []byte) bool {
	if len(data) < 56 {
		return false
	}
	switch string(data[:5]) {
	case "PPF10":
		return data[5] == 0
	case "PPF20":
		return len(data) >= 1084 && data[5] == 1
	case "PPF30":
		return len(data) >= 60 && data[5] == 2
	default:
		return false
	}
}
