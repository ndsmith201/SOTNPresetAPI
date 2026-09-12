package catalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const optionJSON = `{"comment":"Test","category":"gameplay","type":"word","value":"0x01"}`
const testID = "0123456789abcdef0123456789abcdef"

type stubStore struct {
	item  Item
	err   error
	after string
	limit int
	calls int
	vote  int
	page  Page
}

func (s *stubStore) Create(_ context.Context, i Item) error { s.calls++; s.item = i; return s.err }
func (s *stubStore) SaveOption(ctx context.Context, item Item, previous *Item) (Item, error) {
	return s.SavePreset(ctx, item, previous)
}
func (s *stubStore) SavePreset(_ context.Context, item Item, previous *Item) (Item, error) {
	s.calls++
	if previous != nil {
		data := item.Data
		item = *previous
		item.Data = data
	}
	s.item = item
	return item, s.err
}
func (s *stubStore) Get(context.Context, string, string) (Item, error) {
	s.calls++
	return s.item, s.err
}
func (s *stubStore) List(_ context.Context, kind string, n int, after string) (Page, error) {
	s.calls++
	s.limit = n
	s.after = after
	return s.page, s.err
}
func (s *stubStore) Vote(_ context.Context, _, _, _ string, v int) (Item, error) {
	s.calls++
	s.vote = v
	return s.item, s.err
}

func TestAPIBoundaries(t *testing.T) {
	tests := []struct {
		name, method, path, body, user, media string
		status                                int
	}{
		{"health", "GET", "/healthz", "", "", "", 200},
		{"public list", "GET", "/v1/options", "", "", "", 200},
		{"public preset", "GET", "/v1/presets/" + testID, "", "", "", 200},
		{"auth required", "POST", "/v1/options", optionJSON, "", "application/json", 401},
		{"create option", "POST", "/v1/options", optionJSON, "alice", "application/json; charset=utf-8", 201},
		{"create preset", "POST", "/v1/presets", `{"metadata":{"id":"test","name":"Test"},"custom":{"number":9007199254740993}}`, "alice", "application/json", 201},
		{"media type", "POST", "/v1/options", optionJSON, "alice", "text/plain", 415},
		{"oversized", "POST", "/v1/options", strings.Repeat(" ", MaxBodyBytes+1), "alice", "application/json", 413},
		{"invalid JSON", "POST", "/v1/options", "{", "alice", "application/json", 400},
		{"extra JSON", "POST", "/v1/options", optionJSON + " {}", "alice", "application/json", 400},
		{"unknown field", "POST", "/v1/options", strings.TrimSuffix(optionJSON, "}") + `,"createdBy":"bob"}`, "alice", "application/json", 400},
		{"invalid category", "POST", "/v1/options", strings.Replace(optionJSON, "gameplay", "invalid", 1), "alice", "application/json", 400},
		{"null option", "POST", "/v1/options", "null", "alice", "application/json", 400},
		{"missing metadata", "POST", "/v1/presets", "{}", "alice", "application/json", 400},
		{"empty name", "POST", "/v1/presets", `{"metadata":{"id":"test","name":" "}}`, "alice", "application/json", 400},
		{"array preset", "POST", "/v1/presets", "[]", "alice", "application/json", 400},
		{"bad ID", "GET", "/v1/options/anything", "", "", "", 404},
		{"bad route", "GET", "/v1/options/" + testID + "/other", "", "", "", 404},
		{"method", "DELETE", "/v1/options/" + testID, "", "alice", "", 405},
		{"upvote", "PUT", "/v1/options/" + testID + "/vote", `{"value":1}`, "alice", "application/json", 200},
		{"downvote", "PUT", "/v1/presets/" + testID + "/vote", `{"value":-1}`, "alice", "application/json", 200},
		{"remove vote", "PUT", "/v1/options/" + testID + "/vote", `{"value":0}`, "alice", "application/json", 200},
		{"anonymous vote", "PUT", "/v1/options/" + testID + "/vote", `{"value":1}`, "", "application/json", 401},
		{"bad vote", "PUT", "/v1/options/" + testID + "/vote", `{"value":2}`, "alice", "application/json", 400},
		{"missing vote", "PUT", "/v1/options/" + testID + "/vote", `{}`, "alice", "application/json", 400},
		{"null vote", "PUT", "/v1/options/" + testID + "/vote", `{"value":null}`, "alice", "application/json", 400},
		{"fractional vote", "PUT", "/v1/options/" + testID + "/vote", `{"value":0.5}`, "alice", "application/json", 400},
		{"spoofed voter", "PUT", "/v1/options/" + testID + "/vote", `{"value":1,"userId":"bob"}`, "alice", "application/json", 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &stubStore{}
			res := (API{Store: s}).Handle(context.Background(), Request{Method: tt.method, Path: tt.path, Body: []byte(tt.body), Subject: tt.user, ContentType: tt.media})
			if res.Status != tt.status {
				t.Fatalf("status %d, want %d: %s", res.Status, tt.status, res.Body)
			}
			if !json.Valid([]byte(res.Body)) {
				t.Fatal("response is not JSON")
			}
			if tt.status >= 400 && s.calls != 0 {
				t.Fatal("invalid request reached database")
			}
			if tt.status == 201 && (!ValidID(s.item.ID) || s.item.CreatedBy != "alice" || res.Headers["Location"] != tt.path+"/"+s.item.ID) {
				t.Fatalf("bad creation: %+v", s.item)
			}
		})
	}
}

func TestPagination(t *testing.T) {
	cursor := base64.RawURLEncoding.EncodeToString([]byte("options:" + testID))
	s := &stubStore{page: Page{NextCursor: testID}}
	res := (API{s}).Handle(context.Background(), Request{Method: "GET", Path: "/v1/options", Query: map[string]string{"limit": "5", "cursor": cursor}})
	if res.Status != 200 || s.after != testID || s.limit != 5 {
		t.Fatalf("unexpected pagination: %+v %+v", s, res)
	}
	if !strings.Contains(res.Body, `"items":[]`) || !strings.Contains(res.Body, cursor) {
		t.Fatal(res.Body)
	}
	for _, query := range []map[string]string{{"limit": "0"}, {"limit": "51"}, {"limit": "x"}, {"cursor": "bad"}, {"cursor": base64.RawURLEncoding.EncodeToString([]byte("presets:" + testID))}} {
		res := (API{s}).Handle(context.Background(), Request{Method: "GET", Path: "/v1/options", Query: query})
		if res.Status != 400 {
			t.Fatal(res)
		}
	}
}

func TestStoreErrors(t *testing.T) {
	for _, tt := range []struct {
		err    error
		status int
	}{{ErrNotFound, 404}, {ErrConflict, 409}, {errors.New("secret database details"), 500}} {
		res := (API{&stubStore{err: tt.err}}).Handle(context.Background(), Request{Method: "GET", Path: "/v1/options/" + testID})
		if res.Status != tt.status || strings.Contains(res.Body, "secret database") {
			t.Fatal(res)
		}
	}
}

func TestOptionValidationAndPresetPreservation(t *testing.T) {
	oversizedAfterEncoding := `{"comment":"x","category":"gameplay","type":"string","value":"` + strings.Repeat("<", 30000) + `"}`
	if _, err := NewItem("options", "alice", []byte(oversizedAfterEncoding)); err == nil {
		t.Fatal("accepted JSON that exceeds the storage limit after normalization")
	}
	for _, body := range []string{
		`{"comment":"x","category":"relics","type":"char","value":"1","additionalWrites":[null]}`,
		`{"comment":"x","category":"relics","type":"char","value":"[]","rawJson":true}`,
		`{"comment":"x","category":"relics","type":"char","value":"1","address":" "}`,
	} {
		if _, err := NewItem("options", "alice", []byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	item, err := NewItem("options", "alice", []byte(optionJSON))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(item.Data), `"additionalWrites":[]`) || !strings.Contains(string(item.Data), `"address":null`) {
		t.Fatal(string(item.Data))
	}
	preset := `{"metadata":{"id":"x","name":"X"},"futureSetting":9007199254740993,"stats":false}`
	item, err = NewItem("presets", "alice", []byte(preset))
	if err != nil {
		t.Fatal(err)
	}
	if string(item.Data) != preset {
		t.Fatal("preset changed:", string(item.Data))
	}
}
