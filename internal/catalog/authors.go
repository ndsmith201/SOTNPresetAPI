package catalog

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type UsernameResolver interface {
	Username(context.Context, string) (string, error)
}

// Options resolve their author before insertion and read it from storage thereafter.
// Presets retain response-time resolution. Display names never grant ownership.
func (a API) HandleWithAuthors(ctx context.Context, r Request, resolver UsernameResolver) Response {
	if r.Path == "/v1/options" || strings.HasPrefix(r.Path, "/v1/options/") {
		if resolver != nil {
			a.Store = optionAuthorStore{Store: a.Store, resolver: resolver}
		}
		return a.Handle(ctx, r)
	}
	res := a.Handle(ctx, r)
	if resolver == nil || res.Status < 200 || res.Status >= 300 || r.Path == "/healthz" {
		return res
	}
	var page Page
	var item Item
	if err := json.Unmarshal([]byte(res.Body), &page); err != nil {
		return res
	}
	isPage := page.Items != nil
	if !isPage {
		if err := json.Unmarshal([]byte(res.Body), &item); err != nil || item.ID == "" {
			return res
		}
		page.Items = []Item{item}
	}
	// Keep an account-service outage from preventing catalog browsing or making
	// an otherwise successful share/vote appear to fail.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	names := make(map[string]string)
	var subjects []string
	for _, entry := range page.Items {
		if _, exists := names[entry.CreatedBy]; !exists {
			subjects = append(subjects, entry.CreatedBy)
		}
		names[entry.CreatedBy] = ""
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	workers := make(chan struct{}, 4)
	for _, subject := range subjects {
		wg.Go(func() {
			select {
			case workers <- struct{}{}:
				defer func() { <-workers }()
			case <-ctx.Done():
				return
			}
			name, err := resolver.Username(ctx, subject)
			if err != nil {
				slog.Warn("author username lookup failed", "error", err)
				return
			}
			mu.Lock()
			names[subject] = name
			mu.Unlock()
		})
	}
	wg.Wait()
	for i := range page.Items {
		page.Items[i].CreatedByUsername = names[page.Items[i].CreatedBy]
	}
	var value any = page
	if !isPage {
		value = page.Items[0]
	}
	if body, err := json.Marshal(value); err == nil {
		res.Body = string(body)
	}
	return res
}

type optionAuthorStore struct {
	Store
	resolver UsernameResolver
}

func (s optionAuthorStore) SaveOption(ctx context.Context, submitted Item, previous *Item) (Item, error) {
	// Updates keep the original saved author, including legacy rows awaiting backfill.
	if previous == nil {
		lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		name, err := s.resolver.Username(lookupCtx, submitted.CreatedBy)
		if err != nil || strings.TrimSpace(name) == "" {
			slog.Warn("option author lookup failed", "error", err)
			return Item{}, ErrAuthorUnavailable
		}
		submitted.CreatedByUsername = strings.TrimSpace(name)
	}
	return s.Store.SaveOption(ctx, submitted, previous)
}
