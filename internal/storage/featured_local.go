package storage

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"sotnpresetapi/internal/catalog"
)

// LocalAssets is used only by cmd/local; patches are served behind the API's
// release-time check rather than a public file server.
type LocalAssets struct{ Directory, BaseURL string }

func (s LocalAssets) Upload(_ context.Context, key string, data []byte, _ string) error {
	name, err := s.file(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	return os.WriteFile(name, data, 0600)
}

func (s LocalAssets) URL(_ context.Context, key string) (string, error) {
	if _, err := s.file(key); err != nil {
		return "", err
	}
	if strings.HasPrefix(key, "images/") {
		return s.BaseURL + "/featured-mod-images/" + strings.TrimPrefix(key, "images/"), nil
	}
	return s.BaseURL + "/featured-mod-files/" + strings.TrimPrefix(key, "patches/"), nil
}

func (s LocalAssets) Delete(_ context.Context, key string) error {
	name, err := s.file(key)
	if err != nil {
		return err
	}
	err = os.Remove(name)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (s LocalAssets) file(key string) (string, error) {
	parts := strings.Split(key, "/")
	if len(parts) != 2 || (parts[0] != "images" && parts[0] != "patches") {
		return "", errors.New("invalid featured-mod asset key")
	}
	ext := filepath.Ext(parts[1])
	if !catalog.ValidID(strings.TrimSuffix(parts[1], ext)) || (parts[0] == "images" && ext != ".png" && ext != ".jpg" && ext != ".webp") || (parts[0] == "patches" && ext != ".ppf") {
		return "", errors.New("invalid featured-mod asset key")
	}
	return filepath.Join(s.Directory, filepath.FromSlash(key)), nil
}

func (s LocalAssets) ServeImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	key := "images/" + strings.TrimPrefix(r.URL.Path, "/featured-mod-images/")
	name, err := s.file(key)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, name)
}

func (s LocalAssets) ServePatch(api catalog.API, w http.ResponseWriter, r *http.Request) {
	file := strings.TrimPrefix(r.URL.Path, "/featured-mod-files/")
	id := strings.TrimSuffix(file, ".ppf")
	if !strings.HasSuffix(file, ".ppf") || !catalog.ValidID(id) {
		http.NotFound(w, r)
		return
	}
	res := api.Handle(r.Context(), catalog.Request{Method: r.Method, Path: "/v1/featured-mods/" + id + "/download"})
	for k, v := range res.Headers {
		if k != "Location" {
			w.Header().Set(k, v)
		}
	}
	if res.Status != 307 {
		w.WriteHeader(res.Status)
		_, _ = w.Write([]byte(res.Body))
		return
	}
	name, err := s.file("patches/" + file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+file+`"`)
	http.ServeFile(w, r, name)
}
