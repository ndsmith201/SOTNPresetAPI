package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type shareStore struct {
	stubStore
	pages []Page
	reads int
	saves int
}

func (s *shareStore) List(_ context.Context, kind string, limit int, after string) (Page, error) {
	if s.reads > 0 && after != s.pages[s.reads-1].NextCursor {
		panic("lost cursor")
	}
	page := s.pages[s.reads]
	s.reads++
	return page, nil
}
func (s *shareStore) SavePreset(ctx context.Context, item Item, previous *Item) (Item, error) {
	s.saves++
	return s.stubStore.SavePreset(ctx, item, previous)
}
func TestSharePresetAuthorUpdates(t *testing.T) {
	existing := Item{ID: testID, Kind: "presets", CreatedBy: "someone-else", CreatedAt: "original-date", Upvotes: 7, Downvotes: 2, Score: 5,
		Data: json.RawMessage(`{"metadata":{"id":"old-id","name":"Castle Run","author":["bob"," Alice "]},"music":true}`)}
	for _, tc := range []struct {
		name, username, authors string
		status                  int
	}{
		{"coauthor", "alice", `["alice","bob"]`, 200},
		{"username case", "ALICE", `["alice"]`, 200},
		{"forged submitted author", "mallory", `["alice","mallory"]`, 403},
		{"creator is not an author", "someone-else", `["someone-else"]`, 403},
		{"missing verified username", "", `["alice"]`, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &shareStore{pages: []Page{{NextCursor: "page-two"}, {Items: []Item{existing}}}}
			body := `{"metadata":{"id":"new-id","name":" castle RUN ","author":` + tc.authors + `},"music":false,"custom":9007199254740993}`
			res := (API{s}).Handle(context.Background(), Request{Method: "POST", Path: "/v1/presets", ContentType: "application/json", Subject: "verified-sub", Username: tc.username, Body: []byte(body)})
			if res.Status != tc.status {
				t.Fatalf("%+v", res)
			}
			if tc.status != 200 {
				if s.saves != 0 {
					t.Fatal("unauthorized write")
				}
				return
			}
			if s.saves != 1 || s.reads != 2 {
				t.Fatalf("unexpected calls: %+v", s)
			}
			got := s.item
			if got.ID != existing.ID || got.CreatedAt != existing.CreatedAt || got.CreatedBy != existing.CreatedBy || got.Upvotes != 7 || got.Downvotes != 2 || got.Score != 5 {
				t.Fatalf("identity or votes changed: %+v", got)
			}
			if string(got.Data) != body {
				t.Fatal("payload changed")
			}
			if res.Headers["Location"] != "/v1/presets/"+existing.ID {
				t.Fatal("wrong location")
			}
		})
	}
}

func TestSharePresetCreationConflictsAndMalformedAuthors(t *testing.T) {
	body := []byte(`{"metadata":{"id":"new","name":"New","author":["alice"]}}`)
	for _, tc := range []struct {
		name   string
		items  []Item
		err    error
		status int
	}{
		{"new name", nil, nil, 201},
		{"concurrent creation", nil, ErrConflict, 409},
		{"database failure", nil, errors.New("database failure"), 500},
		{"legacy string author", []Item{{ID: testID, Data: json.RawMessage(`{"metadata":{"name":"New","author":"alice"}}`)}}, nil, 403},
		{"missing authors", []Item{{ID: testID, Data: json.RawMessage(`{"metadata":{"name":"New"}}`)}}, nil, 403},
		{"ambiguous legacy duplicates", []Item{{ID: testID, Data: body}, {ID: "abcdef0123456789abcdef0123456789", Data: body}}, nil, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &shareStore{stubStore: stubStore{err: tc.err}, pages: []Page{{Items: tc.items}}}
			res := (API{s}).Handle(context.Background(), Request{Method: "POST", Path: "/v1/presets", ContentType: "application/json", Subject: "verified-sub", Username: "alice", Body: body})
			if res.Status != tc.status {
				t.Fatalf("%+v", res)
			}
			if len(tc.items) > 0 && s.saves != 0 {
				t.Fatal("invalid update reached store")
			}
		})
	}
}

func TestSharePresetRejectsAnyUnauthorizedNameCollision(t *testing.T) {
	owned := Item{ID: testID, Kind: "presets", Data: json.RawMessage(`{"metadata":{"name":"Castle Run","author":["alice"]}}`)}
	other := Item{ID: "abcdef0123456789abcdef0123456789", Kind: "presets", Data: json.RawMessage(`{"metadata":{"name":" castle RUN ","author":["bob"]}}`)}
	for _, tc := range []struct {
		name  string
		pages []Page
	}{
		{"owned first", []Page{{Items: []Item{owned, other}}}},
		{"other first", []Page{{Items: []Item{other, owned}}}},
		{"other on later page", []Page{{Items: []Item{owned}, NextCursor: "page-two"}, {Items: []Item{other}}}},
		{"owned on later page", []Page{{Items: []Item{other}, NextCursor: "page-two"}, {Items: []Item{owned}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &shareStore{pages: tc.pages}
			res := (API{s}).Handle(context.Background(), Request{
				Method: "POST", Path: "/v1/presets", ContentType: "application/json", Subject: "verified-sub", Username: "alice",
				Body: []byte(`{"metadata":{"id":"new","name":"Castle Run","author":["alice","bob"]}}`),
			})
			if res.Status != 403 {
				t.Fatalf("expected forbidden name collision, got %+v", res)
			}
			if s.saves != 0 {
				t.Fatal("unauthorized name collision reached store")
			}
		})
	}
}
