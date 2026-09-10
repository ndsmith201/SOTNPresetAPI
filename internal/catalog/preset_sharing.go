package catalog

import (
	"context"
	"encoding/json"
	"strings"
)

// PresetName is the catalog collision key; metadata.id remains independent.
func PresetName(data json.RawMessage) string {
	var preset struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
	}
	if json.Unmarshal(data, &preset) != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(preset.Metadata.Name))
}

func presetAuthor(data json.RawMessage, username string) bool {
	if strings.TrimSpace(username) == "" {
		return false
	}
	var preset struct {
		Metadata struct {
			Author []string `json:"author"`
		} `json:"metadata"`
	}
	if json.Unmarshal(data, &preset) != nil {
		return false
	}
	for _, author := range preset.Metadata.Author {
		if strings.EqualFold(strings.TrimSpace(author), strings.TrimSpace(username)) {
			return true
		}
	}
	return false
}

func (a API) sharePreset(ctx context.Context, submitted Item, username string) Response {
	// Existing catalogs predate name keys. Follow every page to include those
	// rows without requiring a destructive migration or trusting client IDs.
	var previous *Item
	name := PresetName(submitted.Data)
	after := ""
	seen := map[string]bool{}
	for {
		page, err := a.Store.List(ctx, "presets", 50, after)
		if err != nil {
			return storeError(err)
		}
		for _, item := range page.Items {
			if PresetName(item.Data) != name {
				continue
			}
			// Authorize against the stored authors, never the new submission.
			if !presetAuthor(item.Data, username) {
				return storeError(ErrForbidden)
			}
			if previous != nil {
				return failure(409, "conflict", "multiple presets with this name list you as an author; use a unique preset name")
			}
			copy := item
			previous = &copy
		}
		if page.NextCursor == "" {
			break
		}
		if seen[page.NextCursor] {
			return storeError(ErrConflict)
		}
		seen[page.NextCursor] = true
		after = page.NextCursor
	}
	item, err := a.Store.SavePreset(ctx, submitted, previous)
	if err != nil {
		return storeError(err)
	}
	status := 201
	if previous != nil {
		status = 200
	}
	res := reply(status, item)
	res.Headers["Location"] = "/v1/presets/" + item.ID
	return res
}
