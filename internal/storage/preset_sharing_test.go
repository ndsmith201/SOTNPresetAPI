package storage

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"sotnpresetapi/internal/catalog"
)

func TestDynamoPresetUpdatePreservesVotesAndRejectsStaleAuthors(t *testing.T) {
	d := integrationStore(t)
	ctx := context.Background()
	api := catalog.API{Store: d}
	// Create a legacy row with no name reservation, as in existing deployments.
	old, err := catalog.NewItem("presets", "original-sub", []byte(`{"metadata":{"id":"old","name":"Castle","author":["alice","bob"]},"music":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Vote(ctx, "presets", old.ID, "voter", 1); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"metadata":{"id":"new","name":" CASTLE ","author":["bob"]},"music":false,"custom":9007199254740993}`)
	request := catalog.Request{Method: "POST", Path: "/v1/presets", ContentType: "application/json", Subject: "alice-sub", Username: "alice", Body: body}
	res := api.Handle(ctx, request)
	if res.Status != 200 {
		t.Fatalf("update failed: %+v", res)
	}
	var updated catalog.Item
	if err := json.Unmarshal([]byte(res.Body), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.ID != old.ID || updated.CreatedBy != old.CreatedBy || updated.CreatedAt != old.CreatedAt || updated.Upvotes != 1 || updated.Score != 1 || string(updated.Data) != string(body) {
		t.Fatalf("bad update: %+v", updated)
	}
	// The old author cannot reuse a stale authorization decision after removal.
	if _, err := d.SavePreset(ctx, updated, &old); !errors.Is(err, catalog.ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	if got := api.Handle(ctx, request); got.Status != 403 {
		t.Fatalf("removed author: %+v", got)
	}
	// Existing per-account votes are preserved, not just their aggregate counters.
	if item, err := d.Vote(ctx, "presets", old.ID, "voter", 1); err != nil || item.Upvotes != 1 {
		t.Fatalf("vote lost: %+v %v", item, err)
	}
	page, err := d.List(ctx, "presets", 50, "")
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("duplicate preset: %+v %v", page, err)
	}
	// A vote arriving after the authorization read must survive the update.
	if _, err := d.Vote(ctx, "presets", old.ID, "another-voter", -1); err != nil {
		t.Fatal(err)
	}
	latest, err := d.SavePreset(ctx, updated, &updated)
	if err != nil || latest.Upvotes != 1 || latest.Downvotes != 1 || latest.Score != 0 {
		t.Fatalf("concurrent vote overwritten: %+v %v", latest, err)
	}
}

func TestDynamoConcurrentPresetCreationReservesOneName(t *testing.T) {
	d := integrationStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, name := range []string{"Castle Run", " castle RUN "} {
		item, err := catalog.NewItem("presets", "creator", []byte(`{"metadata":{"id":"castle","name":"`+name+`","author":["alice"]}}`))
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() { defer wg.Done(); _, err := d.SavePreset(ctx, item, nil); results <- err }()
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
	page, err := d.List(ctx, "presets", 50, "")
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("name reservation leaked or duplicate created: %+v %v", page, err)
	}
}
