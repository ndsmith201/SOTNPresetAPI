package storage

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sotnpresetapi/internal/catalog"
)

func TestLocalFeaturedAssetsRoundTrip(t *testing.T) {
	assets := LocalAssets{Directory: t.TempDir(), BaseURL: "http://127.0.0.1:8080"}
	for _, test := range []struct{ prefix, ext, route string }{
		{"images", ".png", "/featured-mod-images/"}, {"images", ".jpg", "/featured-mod-images/"},
		{"images", ".webp", "/featured-mod-images/"}, {"patches", ".ppf", "/featured-mod-files/"},
	} {
		key := test.prefix + "/" + sampleFeaturedMod().ID + test.ext
		data := []byte("local asset bytes")
		if err := assets.Upload(context.Background(), key, data, "ignored"); err != nil {
			t.Fatal(err)
		}
		name := filepath.Join(assets.Directory, filepath.FromSlash(key))
		got, err := os.ReadFile(name)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("file round trip: %s %v", got, err)
		}
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("asset must be private on disk: %v %v", info, err)
		}
		url, err := assets.URL(context.Background(), key)
		if err != nil || url != assets.BaseURL+test.route+sampleFeaturedMod().ID+test.ext {
			t.Fatalf("local URL: %s %v", url, err)
		}
		if err := assets.Delete(context.Background(), key); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("asset was not removed: %v", err)
		}
		if err := assets.Delete(context.Background(), key); err != nil {
			t.Fatalf("deleting an absent asset should be safe: %v", err)
		}
	}
}

func TestLocalFeaturedAssetsRejectInvalidKeys(t *testing.T) {
	assets := LocalAssets{Directory: t.TempDir(), BaseURL: "http://127.0.0.1:8080"}
	id := sampleFeaturedMod().ID
	for _, key := range []string{
		"", "../" + id + ".png", "images/../" + id + ".png", "/images/" + id + ".png",
		"images/" + id + ".ppf", "patches/" + id + ".png", "images/" + id + ".gif",
		"images/" + strings.ToUpper(id) + ".png", "images/" + id + ".PNG", "images/short.png",
		"patches/" + id + ".ppf/extra", "images/" + id + ".png?download=1",
	} {
		t.Run(key, func(t *testing.T) {
			if err := assets.Upload(context.Background(), key, []byte("data"), "ignored"); err == nil {
				t.Fatal("accepted invalid upload key")
			}
			if _, err := assets.URL(context.Background(), key); err == nil {
				t.Fatal("accepted invalid URL key")
			}
			if err := assets.Delete(context.Background(), key); err == nil {
				t.Fatal("accepted invalid delete key")
			}
		})
	}
}

type localFeaturedStore struct {
	catalog.Store
	mod          catalog.FeaturedMod
	getError     error
	signingCalls int
}

func (s *localFeaturedStore) PublishFeaturedMod(context.Context, catalog.FeaturedMod, catalog.FeaturedModImage, []byte) (catalog.FeaturedMod, error) {
	panic("publication should not run in local download tests")
}
func (s *localFeaturedStore) LatestFeaturedMod(context.Context) (catalog.FeaturedMod, error) {
	return s.mod, nil
}
func (s *localFeaturedStore) GetFeaturedMod(_ context.Context, id string) (catalog.FeaturedMod, error) {
	if id != s.mod.ID {
		return catalog.FeaturedMod{}, catalog.ErrNotFound
	}
	return s.mod, s.getError
}
func (s *localFeaturedStore) FeaturedModDownloadURL(context.Context, catalog.FeaturedMod) (string, error) {
	s.signingCalls++
	return "http://127.0.0.1:8080/featured-mod-files/" + s.mod.ID + ".ppf", nil
}

func TestLocalPatchServingChecksReleaseTime(t *testing.T) {
	assets := LocalAssets{Directory: t.TempDir(), BaseURL: "http://127.0.0.1:8080"}
	mod := sampleFeaturedMod()
	patch := []byte("PPF30-local-patch")
	if err := assets.Upload(context.Background(), "patches/"+mod.ID+".ppf", patch, "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, method, path string
		release            time.Time
		want               int
		wantSigning        int
	}{
		{"upcoming direct file", "GET", mod.ID + ".ppf", time.Now().Add(time.Hour), 403, 0},
		{"released direct file", "GET", mod.ID + ".ppf", time.Now().Add(-time.Hour), 200, 1},
		{"invalid file", "GET", "bad.ppf", time.Now().Add(-time.Hour), 404, 0},
		{"image masquerading as file", "GET", mod.ID + ".png", time.Now().Add(-time.Hour), 404, 0},
		{"unsupported method", "POST", mod.ID + ".ppf", time.Now().Add(-time.Hour), 405, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			mod.ReleaseTime = test.release.UTC().Format(time.RFC3339Nano)
			store := &localFeaturedStore{mod: mod}
			response := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, "/featured-mod-files/"+test.path, nil)
			assets.ServePatch(catalog.API{Store: store}, response, request)
			if response.Code != test.want || store.signingCalls != test.wantSigning {
				t.Fatalf("status %d, want %d; signing %d, want %d; body %s", response.Code, test.want, store.signingCalls, test.wantSigning, response.Body.String())
			}
			if test.want == 200 {
				if !bytes.Equal(response.Body.Bytes(), patch) || response.Header().Get("Content-Type") != "application/octet-stream" ||
					response.Header().Get("Content-Disposition") != `attachment; filename="`+mod.ID+`.ppf"` || response.Header().Get("Location") != "" {
					t.Fatalf("incorrect downloadable PPF: %+v %s", response.Header(), response.Body.String())
				}
			} else if bytes.Contains(response.Body.Bytes(), patch) {
				t.Fatal("rejected request exposed the PPF bytes")
			}
		})
	}
}

func TestLocalFeaturedImageServing(t *testing.T) {
	assets := LocalAssets{Directory: t.TempDir(), BaseURL: "http://127.0.0.1:8080"}
	id := sampleFeaturedMod().ID
	image := []byte("test image bytes")
	if err := assets.Upload(context.Background(), "images/"+id+".png", image, "image/png"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, file string
		want         int
	}{
		{"GET", id + ".png", 200}, {"POST", id + ".png", 405}, {"GET", "invalid.png", 404},
		{"GET", id + ".ppf", 404}, {"GET", strings.Repeat("0", 32) + ".png", 404},
	} {
		response := httptest.NewRecorder()
		assets.ServeImages(response, httptest.NewRequest(test.method, "/featured-mod-images/"+test.file, nil))
		if response.Code != test.want {
			t.Fatalf("%s %s: status %d, want %d", test.method, test.file, response.Code, test.want)
		}
		if test.want == http.StatusOK && (!bytes.Equal(response.Body.Bytes(), image) || response.Header().Get("X-Content-Type-Options") != "nosniff") {
			t.Fatalf("incorrect local image response: %+v %s", response.Header(), response.Body.String())
		}
	}
}
