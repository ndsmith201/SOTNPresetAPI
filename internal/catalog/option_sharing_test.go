package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func (s *shareStore) SaveOption(ctx context.Context, item Item, previous *Item) (Item, error) {
	s.saves++
	return s.stubStore.SaveOption(ctx, item, previous)
}

func TestShareOptionOwnershipAndCollisions(t *testing.T) {
	owned := Item{ID: testID, Kind: "options", CreatedBy: "alice-sub", CreatedAt: "original-date", Upvotes: 7, Downvotes: 2, Score: 5, Data: json.RawMessage(optionJSON)}
	other := owned
	other.ID = strings.Repeat("a", 32)
	other.CreatedBy = "bob-sub"
	duplicate := owned
	duplicate.ID = strings.Repeat("b", 32)
	missingAuthor := owned
	missingAuthor.CreatedBy = ""
	for _, tc := range []struct {
		name, subject, username string
		pages                   []Page
		status                  int
	}{
		{"new name", "alice-sub", "alice", []Page{{}}, 201},
		{"author updates after empty page", "alice-sub", "", []Page{{NextCursor: "next"}, {Items: []Item{owned}}}, 200},
		{"different author", "mallory-sub", "alice", []Page{{Items: []Item{owned}}}, 403},
		{"subject is case sensitive", "ALICE-SUB", "alice", []Page{{Items: []Item{owned}}}, 403},
		{"username cannot grant permission", "mallory-sub", "alice-sub", []Page{{Items: []Item{owned}}}, 403},
		{"missing stored author", "alice-sub", "alice", []Page{{Items: []Item{missingAuthor}}}, 403},
		{"ambiguous owned names", "alice-sub", "alice", []Page{{Items: []Item{owned, duplicate}}}, 409},
		{"other author on later page", "alice-sub", "alice", []Page{{Items: []Item{owned}, NextCursor: "next"}, {Items: []Item{other}}}, 403},
		{"other author first", "alice-sub", "alice", []Page{{Items: []Item{other, owned}}}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &shareStore{pages: tc.pages}
			body := []byte(`{"comment":" tEsT ","category":"items","type":"word","value":"2","description":"Updated"}`)
			res := (API{s}).Handle(context.Background(), Request{Method: "POST", Path: "/v1/options", ContentType: "application/json", Subject: tc.subject, Username: tc.username, Body: body})
			if res.Status != tc.status {
				t.Fatalf("unexpected response: %+v", res)
			}
			if tc.status >= 400 {
				if s.saves != 0 {
					t.Fatal("rejected submission reached save")
				}
				return
			}
			if s.saves != 1 || s.reads != len(tc.pages) {
				t.Fatalf("unexpected calls: %+v", s)
			}
			var got Item
			if err := json.Unmarshal([]byte(res.Body), &got); err != nil {
				t.Fatal(err)
			}
			if res.Headers["Location"] != "/v1/options/"+got.ID {
				t.Fatal("wrong location")
			}
			if tc.status == 200 && (got.ID != owned.ID || got.CreatedBy != owned.CreatedBy || got.CreatedAt != owned.CreatedAt || got.Upvotes != 7 || got.Downvotes != 2 || got.Score != 5) {
				t.Fatalf("identity or votes changed: %+v", got)
			}
			normalized, err := validateOption(body)
			if err != nil || string(got.Data) != string(normalized) {
				t.Fatalf("payload not replaced: %s, %v", got.Data, err)
			}
		})
	}
}

func TestShareOptionStoreFailuresAndRepeatedCursor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		pages  []Page
		status int
	}{
		{"concurrent creation", ErrConflict, []Page{{}}, 409},
		{"save failure", errors.New("private database error"), []Page{{}}, 500},
		{"repeated cursor", nil, []Page{{NextCursor: "next"}, {NextCursor: "next"}}, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &shareStore{stubStore: stubStore{err: tc.err}, pages: tc.pages}
			res := (API{s}).Handle(context.Background(), Request{Method: "POST", Path: "/v1/options", ContentType: "application/json", Subject: "alice-sub", Body: []byte(optionJSON)})
			if res.Status != tc.status || strings.Contains(res.Body, "private database error") {
				t.Fatalf("unexpected response: %+v", res)
			}
		})
	}
	res := (API{&stubStore{err: errors.New("list failed")}}).Handle(context.Background(), Request{Method: "POST", Path: "/v1/options", ContentType: "application/json", Subject: "alice-sub", Body: []byte(optionJSON)})
	if res.Status != 500 {
		t.Fatalf("list failure: %+v", res)
	}
}
