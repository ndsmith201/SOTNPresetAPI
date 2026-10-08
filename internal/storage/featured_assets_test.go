package storage

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"sotnpresetapi/internal/catalog"
)

type recordedFeaturedAsset struct {
	key, contentType string
	data             []byte
}

type stubFeaturedAssets struct {
	uploads            []recordedFeaturedAsset
	urls, deletes      []string
	deleteContextError []error
	failUpload         int
	uploadError        error
	urlError           error
	deleteError        error
}

func (s *stubFeaturedAssets) Upload(_ context.Context, key string, data []byte, contentType string) error {
	s.uploads = append(s.uploads, recordedFeaturedAsset{key: key, data: append([]byte(nil), data...), contentType: contentType})
	if len(s.uploads) == s.failUpload {
		return s.uploadError
	}
	return nil
}

func (s *stubFeaturedAssets) URL(_ context.Context, key string) (string, error) {
	s.urls = append(s.urls, key)
	return "https://assets.example/" + key, s.urlError
}

func (s *stubFeaturedAssets) Delete(ctx context.Context, key string) error {
	s.deletes = append(s.deletes, key)
	s.deleteContextError = append(s.deleteContextError, ctx.Err())
	return s.deleteError
}

func TestFeaturedAssetPublication(t *testing.T) {
	for _, format := range []struct{ contentType, ext string }{
		{"image/png", ".png"}, {"image/jpeg", ".jpg"}, {"image/webp", ".webp"},
	} {
		t.Run(format.contentType, func(t *testing.T) {
			mod := sampleFeaturedMod()
			assets := &stubFeaturedAssets{}
			client := &featuredModClient{}
			store := FeaturedMods{Dynamo: Dynamo{Client: client, Table: "test"}, Assets: assets}
			image := catalog.FeaturedModImage{ContentType: format.contentType, Data: []byte("image-data")}
			patch := []byte("PPF30-patch-data")
			got, err := store.PublishFeaturedMod(context.Background(), mod, image, patch)
			imageKey, patchKey := "images/"+mod.ID+format.ext, "patches/"+mod.ID+".ppf"
			if err != nil || got.Image != "https://assets.example/"+imageKey || got.Patch != patchKey {
				t.Fatalf("publication: %+v %v", got, err)
			}
			if len(assets.uploads) != 2 || assets.uploads[0].key != imageKey || assets.uploads[0].contentType != format.contentType || !bytes.Equal(assets.uploads[0].data, image.Data) ||
				assets.uploads[1].key != patchKey || assets.uploads[1].contentType != "application/octet-stream" || !bytes.Equal(assets.uploads[1].data, patch) {
				t.Fatalf("incorrect binary uploads: %+v", assets.uploads)
			}
			if !reflect.DeepEqual(assets.urls, []string{imageKey}) || len(assets.deletes) != 0 {
				t.Fatalf("publication must sign only image and retain uploads: %+v", assets)
			}
			if client.transactionInput == nil || len(client.transactionInput.TransactItems) != 2 {
				t.Fatalf("metadata was not saved: %+v", client.transactionInput)
			}
			var row featuredModRow
			if err := attributevalue.UnmarshalMap(client.transactionInput.TransactItems[0].Put.Item, &row); err != nil || row.Image != imageKey || row.Patch != patchKey {
				t.Fatalf("Dynamo must persist private object keys: %+v %v", row, err)
			}
		})
	}
}

func TestFeaturedAssetFailuresBeforeTransactionCleanUp(t *testing.T) {
	failure := errors.New("asset operation failed")
	for _, test := range []struct {
		name       string
		failUpload int
		urlError   error
	}{
		{"image upload", 1, nil}, {"patch upload", 2, nil}, {"image URL", 0, failure},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			mod := sampleFeaturedMod()
			assets := &stubFeaturedAssets{failUpload: test.failUpload, uploadError: failure, urlError: test.urlError, deleteError: errors.New("cleanup failed")}
			client := &featuredModClient{}
			store := FeaturedMods{Dynamo: Dynamo{Client: client, Table: "test"}, Assets: assets}
			_, err := store.PublishFeaturedMod(ctx, mod, catalog.FeaturedModImage{ContentType: "image/png", Data: []byte("image")}, []byte("patch"))
			if !errors.Is(err, failure) || client.transactionInput != nil {
				t.Fatalf("asset failure reached metadata transaction or changed error: %v %+v", err, client.transactionInput)
			}
			if !reflect.DeepEqual(assets.deletes, []string{"images/" + mod.ID + ".png", "patches/" + mod.ID + ".ppf"}) {
				t.Fatalf("cleanup must attempt both uploads despite failure: %+v", assets.deletes)
			}
			for _, err := range assets.deleteContextError {
				if err != nil {
					t.Fatalf("cleanup inherited canceled request context: %v", err)
				}
			}
		})
	}
}

func TestFeaturedAssetAmbiguousTransactionRetainsFiles(t *testing.T) {
	databaseError := errors.New("connection interrupted after submission")
	assets := &stubFeaturedAssets{}
	client := &featuredModClient{transactionError: databaseError}
	store := FeaturedMods{Dynamo: Dynamo{Client: client, Table: "test"}, Assets: assets}
	_, err := store.PublishFeaturedMod(context.Background(), sampleFeaturedMod(), catalog.FeaturedModImage{ContentType: "image/png", Data: []byte("image")}, []byte("patch"))
	if !errors.Is(err, databaseError) || client.transactionInput == nil || len(assets.deletes) != 0 || len(assets.uploads) != 2 {
		t.Fatalf("uncertain metadata outcome must retain both objects: %v %+v", err, assets)
	}
}

func TestFeaturedAssetReadURLsAndErrors(t *testing.T) {
	mod := sampleFeaturedMod()
	row, err := featuredModRecord(mod)
	if err != nil {
		t.Fatal(err)
	}
	attributes, err := attributevalue.MarshalMap(row)
	if err != nil {
		t.Fatal(err)
	}
	client := &featuredModClient{queryRows: []map[string]types.AttributeValue{attributes}}
	assets := &stubFeaturedAssets{}
	store := FeaturedMods{Dynamo: Dynamo{Client: client, Table: "test"}, Assets: assets}
	got, err := store.LatestFeaturedMod(context.Background())
	if err != nil || got.Image != "https://assets.example/"+mod.Image || !reflect.DeepEqual(assets.urls, []string{mod.Image}) {
		t.Fatalf("metadata image signing: %+v %v %+v", got, err, assets.urls)
	}
	url, err := store.FeaturedModDownloadURL(context.Background(), mod)
	if err != nil || url != "https://assets.example/"+mod.Patch || !reflect.DeepEqual(assets.urls, []string{mod.Image, mod.Patch}) {
		t.Fatalf("patch signing: %s %v %+v", url, err, assets.urls)
	}
	assets.urlError = errors.New("URL signing failed")
	if _, err := store.LatestFeaturedMod(context.Background()); !errors.Is(err, assets.urlError) {
		t.Fatalf("metadata signing error: %v", err)
	}
	if _, err := store.FeaturedModDownloadURL(context.Background(), mod); !errors.Is(err, assets.urlError) {
		t.Fatalf("patch signing error: %v", err)
	}
	client.queryError = errors.New("database failed")
	before := len(assets.urls)
	if _, err := store.LatestFeaturedMod(context.Background()); !errors.Is(err, client.queryError) || len(assets.urls) != before {
		t.Fatalf("database failure still attempted signing: %v", err)
	}
	store.Assets = nil
	if _, err := store.PublishFeaturedMod(context.Background(), mod, catalog.FeaturedModImage{}, nil); err == nil {
		t.Fatal("publication accepted missing asset storage")
	}
	client.queryError = nil
	if _, err := store.LatestFeaturedMod(context.Background()); err == nil {
		t.Fatal("metadata accepted missing asset storage")
	}
	if _, err := store.FeaturedModDownloadURL(context.Background(), mod); err == nil {
		t.Fatal("download accepted missing asset storage")
	}
}

func TestFeaturedAssetInvalidUploadDoesNotWrite(t *testing.T) {
	for _, test := range []struct {
		name, contentType, id string
	}{
		{"unknown image type", "image/gif", sampleFeaturedMod().ID},
		{"invalid ID", "image/png", "bad"},
	} {
		t.Run(test.name, func(t *testing.T) {
			mod := sampleFeaturedMod()
			mod.ID = test.id
			assets := &stubFeaturedAssets{}
			client := &featuredModClient{}
			store := FeaturedMods{Dynamo: Dynamo{Client: client, Table: "test"}, Assets: assets}
			_, err := store.PublishFeaturedMod(context.Background(), mod, catalog.FeaturedModImage{ContentType: test.contentType}, nil)
			if err == nil || len(assets.uploads) != 0 || client.transactionInput != nil {
				t.Fatalf("invalid upload reached storage: %v", err)
			}
		})
	}
}
