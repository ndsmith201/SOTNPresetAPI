package catalog

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestUnifiedWritesPreserveOrderAndProperties(t *testing.T) {
	writes := `[{"type":"word","value":0,"address":"0x1234","comment":"Independent first note","custom":{"keep":true}},{"type":"short","value":"1","comment":"Second note"}]`
	body := `{"comment":"Memory patch","category":"gameplay","gameInit":true,"writes":` + writes + `}`
	item, err := NewItem("options", "alice", []byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(item.Data, &data); err != nil {
		t.Fatal(err)
	}
	if string(data["writes"]) != writes {
		t.Fatalf("write sequence changed: %s", item.Data)
	}
	for _, field := range []string{"type", "value", "address", "primaryWrite", "additionalWrites"} {
		if _, exists := data[field]; exists {
			t.Fatalf("legacy field remains: %s", field)
		}
	}
	if strings.Contains(string(item.Data), `"value":"0"`) {
		t.Fatal("numeric value became a string")
	}
}

func TestLegacyOptionsNormalizeOnCreateAndPublicReads(t *testing.T) {
	legacy := `{"comment":"Patch","category":"gameplay","type":"word","value":"0","primaryWrite":{"type":"word","value":0,"comment":"First note","custom":true},"additionalWrites":[{"type":"short","value":"2"}]}`
	item, err := NewItem("options", "alice", []byte(legacy))
	if err != nil {
		t.Fatal(err)
	}
	expected := `"writes":[{"type":"word","value":0,"comment":"First note","custom":true},{"type":"short","value":"2"}]`
	if !strings.Contains(string(item.Data), expected) {
		t.Fatal(string(item.Data))
	}
	old := Item{ID: testID, Kind: "options", CreatedBy: "alice", Upvotes: 3, Data: json.RawMessage(legacy)}
	store := &stubStore{item: old, page: Page{Items: []Item{old}}}
	for _, path := range []string{"/v1/options", "/v1/options/" + testID, "/v1/options/" + testID + "/vote"} {
		request := Request{Method: "GET", Path: path}
		if strings.HasSuffix(path, "/vote") {
			request.Method = "PUT"
			request.Subject = "alice"
			request.ContentType = "application/json"
			request.Body = []byte(`{"value":1}`)
		}
		res := (API{Store: store}).Handle(context.Background(), request)
		if res.Status != 200 || !strings.Contains(res.Body, expected) || !strings.Contains(res.Body, `"upvotes":3`) {
			t.Fatal(res)
		}
	}
	if string(store.item.Data) != legacy {
		t.Fatal("read changed stored legacy item")
	}
}

func TestUnifiedWriteValidation(t *testing.T) {
	for _, fields := range []string{
		`"writes":[]`, `"writes":null`, `"writes":[null]`, `"writes":[{}]`,
		`"writes":[{"type":"unknown","value":"1"}]`,
		`"writes":[{"type":"word","value":true}]`,
		`"writes":[{"type":"word","value":" "}]`,
		`"writes":[{"type":"word","value":0}],"primaryWrite":{}`,
		`"writes":[{"type":"word","value":0}],"additionalWrites":[]`,
		`"writes":[{"type":"word","value":0}],"value":"0"`,
		`"writes":[{"type":"word","value":0}],"rawJson":true,"value":"{}"`,
		`"writes":[],"rawJson":true,"value":"[]"`,
		`"writes":[` + strings.TrimSuffix(strings.Repeat(`{"type":"word","value":0},`, 258), ",") + `]`,
	} {
		body := `{"comment":"Patch","category":"gameplay",` + fields + `}`
		if _, err := NewItem("options", "alice", []byte(body)); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}
	item, err := NewItem("options", "alice", []byte(`{"comment":"Settings","category":"gameplay","rawJson":true,"value":"{\"enemyDrops\":true}","writes":[]}`))
	if err != nil || !strings.Contains(string(item.Data), `"writes":[]`) {
		t.Fatalf("%s: %v", item.Data, err)
	}
}
