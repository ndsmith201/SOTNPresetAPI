package storage

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"sotnpresetapi/internal/catalog"
)

func TestDynamoOptionUpdatePreservesVotesAndRejectsStaleWrites(t *testing.T) {
	d := integrationStore(t)
	ctx := context.Background()
	api := catalog.API{Store: d}
	// Legacy options have no name reservation.
	old := createItem(t, d, "options")
	if _, err := d.Vote(ctx, "options", old.ID, "voter", 1); err != nil {
		t.Fatal(err)
	}
	request := catalog.Request{Method: "POST", Path: "/v1/options", ContentType: "application/json", Subject: old.CreatedBy,
		Body: []byte(`{"comment":" TEST ","category":"items","type":"word","value":"2"}`)}
	res := api.Handle(ctx, request)
	if res.Status != 200 {
		t.Fatalf("update failed: %+v", res)
	}
	var updated catalog.Item
	if err := json.Unmarshal([]byte(res.Body), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.ID != old.ID || updated.CreatedBy != old.CreatedBy || updated.CreatedAt != old.CreatedAt || updated.Upvotes != 1 || updated.Score != 1 || string(updated.Data) == string(old.Data) {
		t.Fatalf("bad update: %+v", updated)
	}
	if _, err := d.SaveOption(ctx, updated, &old); !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	request.Subject = "another-author"
	if got := api.Handle(ctx, request); got.Status != 403 {
		t.Fatalf("unauthorized update: %+v", got)
	}
	if item, err := d.Vote(ctx, "options", old.ID, "voter", 1); err != nil || item.Upvotes != 1 {
		t.Fatalf("vote lost: %+v %v", item, err)
	}
	if _, err := d.Vote(ctx, "options", old.ID, "another-voter", -1); err != nil {
		t.Fatal(err)
	}
	latest, err := d.SaveOption(ctx, updated, &updated)
	if err != nil || latest.Upvotes != 1 || latest.Downvotes != 1 || latest.Score != 0 {
		t.Fatalf("concurrent vote overwritten: %+v %v", latest, err)
	}
	page, err := d.List(ctx, "options", 50, "")
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("duplicate option or leaked reservation: %+v %v", page, err)
	}
}

func TestDynamoConcurrentOptionCreationReservesOneName(t *testing.T) {
	d := integrationStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"Shortcut", " shortcut "} {
		item, err := catalog.NewItem("options", "creator", []byte(`{"comment":"`+name+`","category":"gameplay","type":"word","value":"1"}`))
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() { defer wg.Done(); _, err := d.SaveOption(ctx, item, nil); results <- err }()
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, catalog.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	page, err := d.List(ctx, "options", 50, "")
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("duplicate option or leaked reservation: %+v %v", page, err)
	}
	// A preset with the same display name uses an independent reservation.
	preset, err := catalog.NewItem("presets", "creator", []byte(`{"metadata":{"id":"shortcut","name":"Shortcut"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SavePreset(ctx, preset, nil); err != nil {
		t.Fatalf("option name blocked preset: %v", err)
	}
}
