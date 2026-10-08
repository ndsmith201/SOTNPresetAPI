package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/gif"
	"image/png"
	"mime/multipart"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

type featuredStoreStub struct {
	Store
	mod                                            FeaturedMod
	image                                          FeaturedModImage
	patch                                          []byte
	err                                            error
	signErr                                        error
	publishCalls, latestCalls, getCalls, signCalls int
	getID                                          string
}

func (s *featuredStoreStub) PublishFeaturedMod(_ context.Context, mod FeaturedMod, upload FeaturedModImage, patch []byte) (FeaturedMod, error) {
	s.publishCalls++
	s.mod, s.image, s.patch = mod, upload, patch
	s.mod.Image = "https://images.example.test/" + mod.ID + ".png"
	s.mod.Patch = "private/featured-mods/" + mod.ID + ".ppf"
	return s.mod, s.err
}

func (s *featuredStoreStub) LatestFeaturedMod(context.Context) (FeaturedMod, error) {
	s.latestCalls++
	return s.mod, s.err
}

func (s *featuredStoreStub) GetFeaturedMod(_ context.Context, id string) (FeaturedMod, error) {
	s.getCalls++
	s.getID = id
	return s.mod, s.err
}

func (s *featuredStoreStub) FeaturedModDownloadURL(context.Context, FeaturedMod) (string, error) {
	s.signCalls++
	return "https://downloads.example.test/patch.ppf?signature=secret", s.signErr
}

type featuredFormPart struct {
	name, filename, contentType string
	data                        []byte
}

func featuredPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func featuredPPF(version string) []byte {
	size, method := 60, byte(2)
	if version == "PPF10" {
		size, method = 56, 0
	}
	if version == "PPF20" {
		size, method = 1084, 1
	}
	data := make([]byte, size)
	copy(data, version)
	data[5] = method
	return data
}

func featuredForm(t *testing.T) []featuredFormPart {
	t.Helper()
	return []featuredFormPart{
		{name: "title", data: []byte("  Castle challenge  ")},
		{name: "description", data: []byte("  A new journey.\nWith new rooms.  ")},
		{name: "releaseTime", data: []byte("2026-10-08T12:00:00.123-07:00")},
		{name: "image", filename: "portrait.png", contentType: "application/octet-stream", data: featuredPNG(t, 2, 3)},
		{name: "ppf", filename: "challenge.ppf", contentType: "application/octet-stream", data: featuredPPF("PPF30")},
	}
}

func featuredMultipart(t *testing.T, fields []featuredFormPart) ([]byte, string) {
	t.Helper()
	var data bytes.Buffer
	w := multipart.NewWriter(&data)
	for _, field := range fields {
		var header textproto.MIMEHeader = make(textproto.MIMEHeader)
		disposition := `form-data; name="` + field.name + `"`
		if field.filename != "" {
			disposition += `; filename="` + field.filename + `"`
		}
		header.Set("Content-Disposition", disposition)
		if field.contentType != "" {
			header.Set("Content-Type", field.contentType)
		}
		part, err := w.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(field.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return data.Bytes(), w.FormDataContentType()
}

func featuredRequest(t *testing.T, fields []featuredFormPart) Request {
	t.Helper()
	body, contentType := featuredMultipart(t, fields)
	return Request{Method: "POST", Path: "/v1/featured-mods", Subject: "publisher-sub", CanPublishFeaturedMods: true, ContentType: contentType, Body: body}
}

func TestFeaturedModPublishPreservesUploadsAndSeparatesMetadata(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 123, time.UTC)
	fields := featuredForm(t)
	store := &featuredStoreStub{}
	res := (API{Store: store}).handleFeaturedMods(context.Background(), featuredRequest(t, fields), now)
	if res.Status != 201 || store.publishCalls != 1 {
		t.Fatalf("publish: %+v; calls %d", res, store.publishCalls)
	}
	if !ValidID(store.mod.ID) || store.mod.CreatedBy != "publisher-sub" || store.mod.CreatedAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("invalid server-generated metadata: %+v", store.mod)
	}
	if store.mod.Title != "Castle challenge" || store.mod.Description != "A new journey.\nWith new rooms." || store.mod.ReleaseTime != "2026-10-08T19:00:00.123Z" {
		t.Fatalf("metadata not normalized: %+v", store.mod)
	}
	if !bytes.Equal(store.image.Data, fields[3].data) || store.image.ContentType != "image/png" || !bytes.Equal(store.patch, fields[4].data) {
		t.Fatal("binary upload changed or image MIME type trusted the request")
	}
	var mod FeaturedMod
	if err := json.Unmarshal([]byte(res.Body), &mod); err != nil {
		t.Fatal(err)
	}
	if mod.DownloadAvailable || mod.DownloadURL != "/v1/featured-mods/"+store.mod.ID+"/download" || mod.Image != store.mod.Image {
		t.Fatalf("incorrect future release response: %+v", mod)
	}
	if strings.Contains(res.Body, "private/featured-mods/") || strings.Contains(res.Body, `"patch"`) || strings.Contains(res.Body, "PPF30") || store.signCalls != 0 {
		t.Fatal("metadata exposed or signed private patch data")
	}
	if res.Headers["Location"] != "/v1/featured-mods" || res.Headers["Cache-Control"] != "no-store" {
		t.Fatal(res.Headers)
	}
}

func TestFeaturedModPublishAuthorizationAndMedia(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Request)
		status int
	}{
		{"no subject", func(r *Request) { r.Subject = "" }, 401},
		{"no publisher group", func(r *Request) { r.CanPublishFeaturedMods = false }, 403},
		{"JSON body", func(r *Request) { r.ContentType = "application/json" }, 415},
		{"invalid Content-Type", func(r *Request) { r.ContentType = "multipart/form-data; boundary=\"" }, 415},
		{"no boundary", func(r *Request) { r.ContentType = "multipart/form-data" }, 400},
		{"invalid multipart", func(r *Request) { r.Body = []byte("not a multipart body") }, 400},
		{"body over limit", func(r *Request) { r.Body = make([]byte, MaxFeaturedModBodyBytes+1) }, 413},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &featuredStoreStub{}
			r := featuredRequest(t, featuredForm(t))
			tt.mutate(&r)
			res := (API{Store: store}).handleFeaturedMods(context.Background(), r, time.Now())
			if res.Status != tt.status || store.publishCalls != 0 || store.latestCalls != 0 || store.signCalls != 0 {
				t.Fatalf("unexpected response or storage side effect: %+v %+v", res, store)
			}
		})
	}
}

func TestFeaturedModInvalidMultipartFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]featuredFormPart) []featuredFormPart
	}{
		{"unknown field", func(f []featuredFormPart) []featuredFormPart {
			return append(f, featuredFormPart{name: "createdBy", data: []byte("spoofed")})
		}},
		{"duplicate title", func(f []featuredFormPart) []featuredFormPart { return append(f, f[0]) }},
		{"missing image", func(f []featuredFormPart) []featuredFormPart { return append(f[:3], f[4]) }},
		{"missing patch", func(f []featuredFormPart) []featuredFormPart { return f[:4] }},
		{"blank title", func(f []featuredFormPart) []featuredFormPart { f[0].data = []byte(" \n "); return f }},
		{"blank description", func(f []featuredFormPart) []featuredFormPart { f[1].data = []byte("\t "); return f }},
		{"title upload", func(f []featuredFormPart) []featuredFormPart { f[0].filename = "title.txt"; return f }},
		{"image text field", func(f []featuredFormPart) []featuredFormPart { f[3].filename = ""; return f }},
		{"image URL", func(f []featuredFormPart) []featuredFormPart {
			f[3].data = []byte("https://example.test/image.png")
			return f
		}},
		{"empty image", func(f []featuredFormPart) []featuredFormPart { f[3].data = nil; return f }},
		{"corrupt image", func(f []featuredFormPart) []featuredFormPart { f[3].data = f[3].data[:len(f[3].data)-12]; return f }},
		{"too many image pixels", func(f []featuredFormPart) []featuredFormPart { f[3].data = featuredPNG(t, 4097, 1); return f }},
		{"GIF image", func(f []featuredFormPart) []featuredFormPart {
			var b bytes.Buffer
			if err := gif.Encode(&b, image.NewGray(image.Rect(0, 0, 1, 1)), nil); err != nil {
				t.Fatal(err)
			}
			f[3].data = b.Bytes()
			return f
		}},
		{"patch text field", func(f []featuredFormPart) []featuredFormPart { f[4].filename = ""; return f }},
		{"wrong patch extension", func(f []featuredFormPart) []featuredFormPart { f[4].filename = "patch.bin"; return f }},
		{"wrong patch header", func(f []featuredFormPart) []featuredFormPart { f[4].data = make([]byte, 60); return f }},
		{"wrong patch version method", func(f []featuredFormPart) []featuredFormPart { f[4].data[5] = 1; return f }},
		{"short PPF3", func(f []featuredFormPart) []featuredFormPart { f[4].data = f[4].data[:59]; return f }},
		{"invalid timestamp", func(f []featuredFormPart) []featuredFormPart { f[2].data = []byte("tomorrow"); return f }},
		{"timestamp without timezone", func(f []featuredFormPart) []featuredFormPart { f[2].data = []byte("2026-10-08T12:00:00"); return f }},
		{"timestamp offset hour twenty four", func(f []featuredFormPart) []featuredFormPart {
			f[2].data = []byte("2026-10-08T12:00:00+24:00")
			return f
		}},
		{"timestamp offset minute sixty", func(f []featuredFormPart) []featuredFormPart {
			f[2].data = []byte("2026-10-08T12:00:00+01:60")
			return f
		}},
		{"timestamp comma fraction", func(f []featuredFormPart) []featuredFormPart {
			f[2].data = []byte("2026-10-08T12:00:00,123Z")
			return f
		}},
		{"timestamp single digit hour", func(f []featuredFormPart) []featuredFormPart { f[2].data = []byte("2026-10-08T1:00:00Z"); return f }},
		{"year zero", func(f []featuredFormPart) []featuredFormPart { f[2].data = []byte("0000-01-01T00:00:00Z"); return f }},
		{"UTC normalization year zero", func(f []featuredFormPart) []featuredFormPart {
			f[2].data = []byte("0001-01-01T00:00:00+01:00")
			return f
		}},
		{"UTC normalization year ten thousand", func(f []featuredFormPart) []featuredFormPart {
			f[2].data = []byte("9999-12-31T23:59:59-01:00")
			return f
		}},
		{"invalid UTF-8 title", func(f []featuredFormPart) []featuredFormPart { f[0].data = []byte{'a', 0xff}; return f }},
		{"invalid UTF-8 description", func(f []featuredFormPart) []featuredFormPart { f[1].data = []byte{'a', 0xff}; return f }},
		{"invalid UTF-8 release time", func(f []featuredFormPart) []featuredFormPart { f[2].data = []byte{'a', 0xff}; return f }},
		{"title over limit", func(f []featuredFormPart) []featuredFormPart { f[0].data = bytes.Repeat([]byte("x"), 201); return f }},
		{"description over limit", func(f []featuredFormPart) []featuredFormPart { f[1].data = bytes.Repeat([]byte("x"), 10001); return f }},
		{"release time over limit", func(f []featuredFormPart) []featuredFormPart { f[2].data = bytes.Repeat([]byte("x"), 101); return f }},
		{"image over limit", func(f []featuredFormPart) []featuredFormPart {
			f[3].data = append(f[3].data, make([]byte, MaxFeaturedModImageBytes+1-len(f[3].data))...)
			return f
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &featuredStoreStub{}
			r := featuredRequest(t, tt.mutate(featuredForm(t)))
			res := (API{Store: store}).handleFeaturedMods(context.Background(), r, time.Now())
			if res.Status != 400 || store.publishCalls != 0 {
				t.Fatalf("invalid upload reached storage: %+v; calls %d", res, store.publishCalls)
			}
		})
	}
}

func TestFeaturedModUploadSizeBoundariesAndPPFVersions(t *testing.T) {
	for _, version := range []string{"PPF10", "PPF20", "PPF30"} {
		t.Run(version, func(t *testing.T) {
			fields := featuredForm(t)
			fields[4].data = featuredPPF(version)
			fields[4].filename = "PATCH.PPF"
			res := (API{Store: &featuredStoreStub{}}).handleFeaturedMods(context.Background(), featuredRequest(t, fields), time.Now())
			if res.Status != 201 {
				t.Fatal(res)
			}
		})
	}
	for _, kind := range []string{"title", "description", "image", "ppf"} {
		t.Run(kind+" at limit", func(t *testing.T) {
			fields := featuredForm(t)
			switch kind {
			case "title":
				fields[0].data = bytes.Repeat([]byte("x"), 200)
			case "description":
				fields[1].data = bytes.Repeat([]byte("x"), 10000)
			case "image":
				fields[3].data = append(fields[3].data, make([]byte, MaxFeaturedModImageBytes-len(fields[3].data))...)
			case "ppf":
				fields[4].data = append(fields[4].data, make([]byte, MaxFeaturedModPatchBytes-len(fields[4].data))...)
			}
			res := (API{Store: &featuredStoreStub{}}).handleFeaturedMods(context.Background(), featuredRequest(t, fields), time.Now())
			if res.Status != 201 {
				t.Fatal(res)
			}
		})
	}
	fields := featuredForm(t)
	fields[4].data = append(fields[4].data, make([]byte, MaxFeaturedModPatchBytes+1-len(fields[4].data))...)
	res := (API{Store: &featuredStoreStub{}}).handleFeaturedMods(context.Background(), featuredRequest(t, fields), time.Now())
	if res.Status != 400 || !strings.Contains(res.Body, "ppf exceeds") {
		t.Fatal(res)
	}
}

func TestFeaturedModTotalMultipartBodySizeBoundary(t *testing.T) {
	fields := featuredForm(t)
	fields[3].data = append(fields[3].data, make([]byte, MaxFeaturedModImageBytes-len(fields[3].data))...)
	initial := featuredRequest(t, fields)
	fields[4].data = append(fields[4].data, make([]byte, MaxFeaturedModBodyBytes-len(initial.Body))...)
	request := featuredRequest(t, fields)
	if len(request.Body) != MaxFeaturedModBodyBytes {
		t.Fatalf("fixture has %d body bytes, want %d", len(request.Body), MaxFeaturedModBodyBytes)
	}
	store := &featuredStoreStub{}
	res := (API{Store: store}).handleFeaturedMods(context.Background(), request, time.Now())
	if res.Status != 201 || store.publishCalls != 1 {
		t.Fatalf("at-limit multipart upload rejected: %+v", res)
	}
	fields[4].data = append(fields[4].data, 0)
	request = featuredRequest(t, fields)
	store = &featuredStoreStub{}
	res = (API{Store: store}).handleFeaturedMods(context.Background(), request, time.Now())
	if res.Status != 413 || store.publishCalls != 0 {
		t.Fatalf("over-limit multipart upload reached storage: %+v", res)
	}
}

func TestFeaturedModMetadataReleaseBoundary(t *testing.T) {
	release := time.Date(2026, 10, 8, 19, 0, 0, 123, time.UTC)
	mod := FeaturedMod{ID: testID, Title: "Future mod", Description: "Available soon", Image: "https://images.example.test/a.png", ReleaseTime: release.Format(time.RFC3339Nano), Patch: "private/patch.ppf", CreatedBy: "publisher"}
	for _, tt := range []struct {
		name      string
		now       time.Time
		available bool
	}{
		{"before release", release.Add(-time.Nanosecond), false},
		{"at release", release, true},
		{"after release", release.Add(time.Nanosecond), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &featuredStoreStub{mod: mod}
			res := (API{Store: store}).handleFeaturedMods(context.Background(), Request{Method: "GET", Path: "/v1/featured-mods"}, tt.now)
			var got FeaturedMod
			if err := json.Unmarshal([]byte(res.Body), &got); err != nil {
				t.Fatal(err)
			}
			if res.Status != 200 || got.Title != mod.Title || got.DownloadAvailable != tt.available || got.DownloadURL != "/v1/featured-mods/"+testID+"/download" {
				t.Fatalf("wrong metadata: %+v %+v", res, got)
			}
			if store.latestCalls != 1 || store.signCalls != 0 || strings.Contains(res.Body, "private/") || strings.Contains(res.Body, `"patch"`) {
				t.Fatal("metadata fetched or leaked patch")
			}
		})
	}
}

func TestFeaturedModDownloadEnforcesReleaseOnServer(t *testing.T) {
	release := time.Date(2026, 10, 8, 19, 0, 0, 123, time.UTC)
	for _, tt := range []struct {
		name              string
		now               time.Time
		status, signCalls int
	}{
		{"before release", release.Add(-time.Nanosecond), 403, 0},
		{"at release", release, 307, 1},
		{"after release", release.Add(time.Nanosecond), 307, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &featuredStoreStub{mod: FeaturedMod{ID: testID, ReleaseTime: release.Format(time.RFC3339Nano), Patch: "private/a.ppf"}}
			res := (API{Store: store}).handleFeaturedModDownload(context.Background(), Request{Method: "GET", Path: "/v1/featured-mods/" + testID + "/download"}, tt.now)
			if res.Status != tt.status || store.getCalls != 1 || store.getID != testID || store.signCalls != tt.signCalls {
				t.Fatalf("wrong download result: %+v %+v", res, store)
			}
			if tt.status == 307 && (res.Headers["Location"] != "https://downloads.example.test/patch.ppf?signature=secret" || res.Headers["Cache-Control"] != "no-store" || res.Body != "") {
				t.Fatal(res)
			}
			if tt.status == 403 && (res.Headers["Location"] != "" || !strings.Contains(res.Body, "not_released")) {
				t.Fatal(res)
			}
		})
	}
}

func TestFeaturedModRoutesAndMethods(t *testing.T) {
	for _, tt := range []struct {
		method, path string
		status       int
		allow        string
	}{
		{"DELETE", "/v1/featured-mods", 405, "GET, POST"},
		{"POST", "/v1/featured-mods/" + testID + "/download", 405, "GET"},
		{"GET", "/v1/featured-mods/bad/download", 404, ""},
		{"GET", "/v1/featured-mods/" + strings.ToUpper(testID) + "/download", 404, ""},
		{"GET", "/v1/featured-mods/" + testID, 404, ""},
		{"GET", "/v1/featured-mods/" + testID + "/other", 404, ""},
		{"GET", "/v1/featured-mods/" + testID + "/download/extra", 404, ""},
	} {
		t.Run(tt.method+tt.path, func(t *testing.T) {
			store := &featuredStoreStub{}
			res := (API{Store: store}).Handle(context.Background(), Request{Method: tt.method, Path: tt.path})
			if res.Status != tt.status || res.Headers["Allow"] != tt.allow || store.latestCalls+store.getCalls+store.signCalls+store.publishCalls != 0 {
				t.Fatalf("%+v %+v", res, store)
			}
		})
	}
}

func TestFeaturedModStorageFailures(t *testing.T) {
	for _, tt := range []struct {
		name   string
		err    error
		status int
	}{
		{"missing", ErrNotFound, 404}, {"conflict", ErrConflict, 409}, {"internal", errors.New("secret bucket details"), 500},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &featuredStoreStub{err: tt.err}
			api := API{Store: store}
			requests := []Request{featuredRequest(t, featuredForm(t)), {Method: "GET", Path: "/v1/featured-mods"}, {Method: "GET", Path: "/v1/featured-mods/" + testID + "/download"}}
			for _, request := range requests {
				res := api.Handle(context.Background(), request)
				if res.Status != tt.status || strings.Contains(res.Body, "secret bucket") {
					t.Fatal(res)
				}
			}
			if store.signCalls != 0 {
				t.Fatal("failed lookup reached signer")
			}
		})
	}
	for _, path := range []string{"/v1/featured-mods", "/v1/featured-mods/" + testID + "/download"} {
		res := (API{Store: &stubStore{}}).Handle(context.Background(), Request{Method: "GET", Path: path})
		if res.Status != 500 {
			t.Fatal(res)
		}
		store := &featuredStoreStub{mod: FeaturedMod{ID: testID, ReleaseTime: "invalid"}}
		res = (API{Store: store}).Handle(context.Background(), Request{Method: "GET", Path: path})
		if res.Status != 500 || store.signCalls != 0 {
			t.Fatal(res)
		}
	}
	store := &featuredStoreStub{mod: FeaturedMod{ID: testID, ReleaseTime: "2000-01-01T00:00:00Z"}, signErr: errors.New("secret signing details")}
	res := (API{Store: store}).Handle(context.Background(), Request{Method: "GET", Path: "/v1/featured-mods/" + testID + "/download"})
	if res.Status != 500 || strings.Contains(res.Body, "secret signing") || res.Headers["Location"] != "" {
		t.Fatal(res)
	}
}

func TestFeaturedModMetadataBypassesAuthorResolution(t *testing.T) {
	store := &featuredStoreStub{mod: FeaturedMod{ID: testID, Title: "Title", ReleaseTime: "2099-01-01T00:00:00Z"}}
	calls := 0
	resolver := resolverFunc(func(context.Context, string) (string, error) { calls++; return "name", nil })
	res := (API{Store: store}).HandleWithAuthors(context.Background(), Request{Method: "GET", Path: "/v1/featured-mods"}, resolver)
	if res.Status != 200 || calls != 0 || !strings.Contains(res.Body, `"title":"Title"`) {
		t.Fatalf("featured metadata altered by author resolution: %+v; calls %d", res, calls)
	}
}
