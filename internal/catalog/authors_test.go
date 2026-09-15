package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type resolverFunc func(context.Context, string) (string, error)

func (f resolverFunc) Username(ctx context.Context, sub string) (string, error) { return f(ctx, sub) }

func TestAuthorDisplayNamesKeepIdentityAndPayload(t *testing.T) {
	stored := Item{ID: testID, Kind: "presets", CreatedBy: "original-sub", Data: json.RawMessage(`{"metadata":{"id":"test","name":"Test"},"large":9007199254740993}`)}
	store := &stubStore{item: stored, page: Page{Items: []Item{stored, stored}, NextCursor: testID}}
	api := API{store}
	calls := 0
	resolver := resolverFunc(func(_ context.Context, sub string) (string, error) {
		calls++
		if sub != stored.CreatedBy {
			t.Fatalf("wrong author %q", sub)
		}
		return "runner", nil
	})
	res := api.HandleWithAuthors(context.Background(), Request{Method: "GET", Path: "/v1/presets"}, resolver)
	var page Page
	if err := json.Unmarshal([]byte(res.Body), &page); err != nil || res.Status != 200 {
		t.Fatal(res, err)
	}
	if calls != 1 || page.NextCursor == "" || len(page.Items) != 2 {
		t.Fatal("pagination/deduplication failed", page, calls)
	}
	for _, item := range page.Items {
		if item.CreatedBy != stored.CreatedBy || item.CreatedByUsername != "runner" || string(item.Data) != string(stored.Data) {
			t.Fatalf("modified identity or payload: %+v", item)
		}
	}
	if store.page.Items[0].CreatedByUsername != "" {
		t.Fatal("mutated stored item")
	}
	for _, request := range []Request{
		{Method: "GET", Path: "/v1/presets/" + testID},
		{Method: "PUT", Path: "/v1/presets/" + testID + "/vote", Subject: "voter", ContentType: "application/json", Body: []byte(`{"value":1}`)},
	} {
		res := api.HandleWithAuthors(context.Background(), request, resolver)
		var item Item
		if err := json.Unmarshal([]byte(res.Body), &item); err != nil || item.CreatedByUsername != "runner" || item.CreatedBy != stored.CreatedBy {
			t.Fatal(res, err)
		}
	}
}

func TestAuthorFailurePreventsUnnamedOptionInsert(t *testing.T) {
	store := &stubStore{}
	res := (API{store}).HandleWithAuthors(context.Background(), Request{Method: "POST", Path: "/v1/options", Subject: "creator-sub", ContentType: "application/json", Body: []byte(optionJSON)}, resolverFunc(func(context.Context, string) (string, error) { return "", errors.New("Cognito unavailable") }))
	if res.Status != 503 || res.Headers["Location"] != "" || store.item.ID != "" {
		t.Fatal(res, store.item)
	}
}

func TestOptionsSaveAuthorOnceAndNeverResolveOnReadsOrUpdates(t *testing.T) {
	store := &stubStore{}
	api := API{store}
	calls := 0
	resolver := resolverFunc(func(_ context.Context, sub string) (string, error) {
		calls++
		if sub != "creator-sub" {
			t.Fatal("wrong lookup identity", sub)
		}
		return "runner", nil
	})
	request := Request{Method: "POST", Path: "/v1/options", Subject: "creator-sub", Username: "untrusted-display-name", ContentType: "application/json", Body: []byte(optionJSON)}
	res := api.HandleWithAuthors(context.Background(), request, resolver)
	if res.Status != 201 || calls != 1 || store.item.CreatedByUsername != "runner" || store.item.CreatedBy != "creator-sub" {
		t.Fatal(res, store.item, calls)
	}
	store.page = Page{Items: []Item{store.item}}
	for _, request := range []Request{
		{Method: "GET", Path: "/v1/options"},
		{Method: "GET", Path: "/v1/options/" + store.item.ID},
		{Method: "PUT", Path: "/v1/options/" + store.item.ID + "/vote", Subject: "voter", ContentType: "application/json", Body: []byte(`{"value":1}`)},
		request,
	} {
		res := api.HandleWithAuthors(context.Background(), request, resolver)
		if res.Status != 200 || !strings.Contains(res.Body, `"createdByUsername":"runner"`) || calls != 1 {
			t.Fatal(res, calls)
		}
	}
	// Legacy options remain readable without a Cognito call until the backfill runs.
	store.item.CreatedByUsername = ""
	res = api.HandleWithAuthors(context.Background(), Request{Method: "GET", Path: "/v1/options/" + store.item.ID}, resolver)
	if res.Status != 200 || calls != 1 {
		t.Fatal(res, calls)
	}
	request.Subject = "another-sub"
	if res := api.HandleWithAuthors(context.Background(), request, resolver); res.Status != 403 || calls != 1 {
		t.Fatal(res, calls)
	}
}
