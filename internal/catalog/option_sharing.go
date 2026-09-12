package catalog

import (
	"context"
	"encoding/json"
	"strings"
)

// OptionName uses the option's display name as the catalog collision key.
func OptionName(data json.RawMessage) string {
	var option struct {
		Comment string `json:"comment"`
	}
	if json.Unmarshal(data, &option) != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(option.Comment))
}

func (a API) shareOption(ctx context.Context, submitted Item) Response {
	// Search every page so existing rows need no migration to name reservations.
	var previous *Item
	name := OptionName(submitted.Data)
	after := ""
	seen := map[string]bool{}
	for {
		page, err := a.Store.List(ctx, "options", 50, after)
		if err != nil {
			return storeError(err)
		}
		for _, item := range page.Items {
			if OptionName(item.Data) != name {
				continue
			}
			// Authorship is the stored Cognito subject, never a submitted field.
			if submitted.CreatedBy == "" || item.CreatedBy != submitted.CreatedBy {
				return failure(403, "forbidden", "an option with this name exists; only its author can update it")
			}
			if previous != nil {
				return failure(409, "conflict", "multiple options with this name belong to you; use a unique option name")
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
	item, err := a.Store.SaveOption(ctx, submitted, previous)
	if err != nil {
		return storeError(err)
	}
	status := 201
	if previous != nil {
		status = 200
	}
	res := reply(status, item)
	res.Headers["Location"] = "/v1/options/" + item.ID
	return res
}
