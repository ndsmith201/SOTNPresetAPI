package catalog

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMainBlockPublishingAndReads(t *testing.T) {
	writes := `[{"type":"word","address":"0x1000","value":0,"comment":"Keep first note","custom":{"keep":true}},{"type":"word","value":"0x34020001"}]`
	s := &stubStore{}
	api := API{Store: s}
	var id string
	for index, flag := range []string{"true", "false"} {
		body := `{"comment":"Main block patch","category":"gameplay","gameInit":false,"itemInit":false,"mainBlock":` + flag + `,"statEdit":false,"rawJson":false,"writes":` + writes + `}`
		res := api.Handle(context.Background(), Request{Method: "POST", Path: "/v1/options", ContentType: "application/json", Subject: "alice", Body: []byte(body)})
		wantStatus := 201
		if index > 0 {
			wantStatus = 200
		}
		if res.Status != wantStatus {
			t.Fatalf("publish status %d, want %d: %s", res.Status, wantStatus, res.Body)
		}
		if id != "" && id != s.item.ID {
			t.Fatal("updating mainBlock replaced the shared option ID")
		}
		id = s.item.ID
		if !strings.Contains(string(s.item.Data), `"mainBlock":`+flag) || !strings.Contains(string(s.item.Data), `"writes":`+writes) {
			t.Fatalf("stored option lost placement or writes: %s", s.item.Data)
		}
		s.page = Page{Items: []Item{s.item}}
		responses := []Response{res}
		for _, path := range []string{"/v1/options/" + id, "/v1/options"} {
			read := api.Handle(context.Background(), Request{Method: "GET", Path: path})
			if read.Status != 200 {
				t.Fatal(read)
			}
			responses = append(responses, read)
		}
		for _, response := range responses {
			if !strings.Contains(response.Body, `"mainBlock":`+flag) || !strings.Contains(response.Body, `"writes":`+writes) {
				t.Fatalf("response lost placement or writes: %s", response.Body)
			}
		}
	}
}

func TestMainBlockDefaultsAndValidation(t *testing.T) {
	for _, base := range []string{optionJSON, `{"comment":"Patch","category":"world","writes":[{"type":"word","value":0}]}`} {
		for _, field := range []string{"", `,"mainBlock":false`, `,"mainBlock":null`, `,"mainBlock":true`} {
			body := strings.TrimSuffix(base, "}") + field + "}"
			item, err := NewItem("options", "alice", []byte(body))
			if err != nil {
				t.Fatal(err)
			}
			var option Option
			if err := json.Unmarshal(item.Data, &option); err != nil {
				t.Fatal(err)
			}
			if option.MainBlock != strings.Contains(field, "true") || len(option.Writes) != 1 {
				t.Fatalf("incorrect normalization: %s", item.Data)
			}
			stored := Item{Kind: "options", Data: json.RawMessage(body)}
			if string(canonicalOptionItem(stored).Data) != string(item.Data) {
				t.Fatal("public read normalized the option differently")
			}
		}
		for _, field := range []string{`,"mainBlock":"true"`, `,"mainBlock":1`, `,"mainBlock":{}`, `,"mainBlock":[]`, `,"mainBlock":true,"unknownPlacement":true`} {
			s := &stubStore{}
			res := (API{Store: s}).Handle(context.Background(), Request{Method: "POST", Path: "/v1/options", ContentType: "application/json", Subject: "alice", Body: []byte(strings.TrimSuffix(base, "}") + field + "}")})
			if res.Status != 400 || s.calls != 0 {
				t.Fatalf("invalid placement reached storage: %+v", res)
			}
		}
	}
}
