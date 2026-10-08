package transport

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"sotnpresetapi/internal/catalog"
)

type featuredCaptureStore struct {
	catalog.Store
	mod          catalog.FeaturedMod
	image        catalog.FeaturedModImage
	patch        []byte
	publishCalls int
}

func (s *featuredCaptureStore) PublishFeaturedMod(_ context.Context, mod catalog.FeaturedMod, image catalog.FeaturedModImage, patch []byte) (catalog.FeaturedMod, error) {
	s.publishCalls++
	s.mod, s.image, s.patch = mod, image, patch
	s.mod.Image = "https://example.test/image.png"
	s.mod.Patch = "private/patch.ppf"
	return s.mod, nil
}
func (s *featuredCaptureStore) LatestFeaturedMod(context.Context) (catalog.FeaturedMod, error) {
	return s.mod, nil
}
func (s *featuredCaptureStore) GetFeaturedMod(context.Context, string) (catalog.FeaturedMod, error) {
	return s.mod, nil
}
func (s *featuredCaptureStore) FeaturedModDownloadURL(context.Context, catalog.FeaturedMod) (string, error) {
	return "https://example.test/patch.ppf", nil
}

func featuredUploadEvent(t *testing.T) (events.APIGatewayV2HTTPRequest, []byte, []byte) {
	t.Helper()
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewNRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	patch := make([]byte, 60)
	copy(patch, "PPF30")
	patch[5] = 2
	patch[6] = 0xff // Verify API Gateway base64 decoding preserves non-UTF-8 bytes.
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, field := range []struct{ name, value string }{
		{"title", "Featured castle"}, {"description", "A community PPF patch"}, {"releaseTime", "2099-01-01T00:00:00Z"},
	} {
		if err := w.WriteField(field.name, field.value); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []struct {
		name, filename string
		data           []byte
	}{
		{"image", "portrait.png", imageData.Bytes()}, {"ppf", "castle.ppf", patch},
	} {
		part, err := w.CreateFormFile(field.name, field.filename)
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
	event := events.APIGatewayV2HTTPRequest{RawPath: "/v1/featured-mods", Headers: map[string]string{"CoNtEnT-TyPe": w.FormDataContentType()}, Body: base64.StdEncoding.EncodeToString(body.Bytes()), IsBase64Encoded: true}
	event.RequestContext.HTTP.Method = "POST"
	return event, imageData.Bytes(), patch
}

func featuredClaims(event *events.APIGatewayV2HTTPRequest, claims map[string]string) {
	event.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{Claims: claims}}
}

func TestLambdaFeaturedModPublisherTrustAndBinaryUpload(t *testing.T) {
	for _, groups := range []string{
		`["featured-mod-publishers"]`,
		`["other","featured-mod-publishers"]`,
		"[other featured-mod-publishers]",
		"[other, featured-mod-publishers]",
	} {
		t.Run(groups, func(t *testing.T) {
			store := &featuredCaptureStore{}
			event, imageData, patch := featuredUploadEvent(t)
			event.Headers["X-Dev-User"] = "spoofed-subject"
			featuredClaims(&event, map[string]string{"sub": "verified-publisher", "username": "publisher", "token_use": "access", "cognito:groups": groups})
			res, err := LambdaHandler(catalog.API{Store: store})(context.Background(), event)
			if err != nil || res.StatusCode != 201 || store.publishCalls != 1 || store.mod.CreatedBy != "verified-publisher" {
				t.Fatalf("publish did not use trusted claims: %+v %v; %+v", res, err, store)
			}
			if !bytes.Equal(store.image.Data, imageData) || !bytes.Equal(store.patch, patch) || store.image.ContentType != "image/png" {
				t.Fatal("multipart binary bytes were not preserved")
			}
			var mod catalog.FeaturedMod
			if err := json.Unmarshal([]byte(res.Body), &mod); err != nil {
				t.Fatal(err)
			}
			if mod.Title != "Featured castle" || mod.DownloadAvailable || mod.DownloadURL != "/v1/featured-mods/"+mod.ID+"/download" || mod.Patch != "" {
				t.Fatalf("bad public metadata: %+v", mod)
			}
		})
	}
}

func TestLambdaFeaturedModRejectsUntrustedPublishers(t *testing.T) {
	tests := []struct {
		name   string
		claims map[string]string
		status int
	}{
		{"no authorizer", nil, 401},
		{"ID token", map[string]string{"sub": "publisher", "token_use": "id", "cognito:groups": `["featured-mod-publishers"]`}, 401},
		{"missing token use", map[string]string{"sub": "publisher", "cognito:groups": `["featured-mod-publishers"]`}, 401},
		{"empty subject", map[string]string{"sub": "", "token_use": "access", "cognito:groups": `["featured-mod-publishers"]`}, 401},
		{"ordinary access token", map[string]string{"sub": "publisher", "token_use": "access"}, 403},
		{"wrong group", map[string]string{"sub": "publisher", "token_use": "access", "cognito:groups": `["admins"]`}, 403},
		{"group name substring", map[string]string{"sub": "publisher", "token_use": "access", "cognito:groups": `["other-featured-mod-publishers"]`}, 403},
		{"group in wrong claim", map[string]string{"sub": "publisher", "token_use": "access", "groups": `["featured-mod-publishers"]`}, 403},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &featuredCaptureStore{}
			event, _, _ := featuredUploadEvent(t)
			event.Headers["Authorization"] = "Bearer fake"
			event.Headers["X-Dev-User"] = "publisher"
			event.Headers["cognito:groups"] = `["featured-mod-publishers"]`
			if tt.claims != nil {
				featuredClaims(&event, tt.claims)
			}
			res, err := LambdaHandler(catalog.API{Store: store})(context.Background(), event)
			if err != nil || res.StatusCode != tt.status || store.publishCalls != 0 {
				t.Fatalf("untrusted publisher reached storage: %+v %v; calls %d", res, err, store.publishCalls)
			}
		})
	}
}

func TestLambdaFeaturedModInvalidBase64DoesNotPublish(t *testing.T) {
	store := &featuredCaptureStore{}
	event, _, _ := featuredUploadEvent(t)
	featuredClaims(&event, map[string]string{"sub": "publisher", "token_use": "access", "cognito:groups": `["featured-mod-publishers"]`})
	event.Body = "%%%"
	res, err := LambdaHandler(catalog.API{Store: store})(context.Background(), event)
	if err != nil || res.StatusCode != 400 || store.publishCalls != 0 || !json.Valid([]byte(res.Body)) {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestLambdaFeaturedMetadataRemainsPublic(t *testing.T) {
	store := &featuredCaptureStore{mod: catalog.FeaturedMod{ID: "0123456789abcdef0123456789abcdef", Title: "Featured castle", Description: "Available soon", Image: "https://example.test/image.png", ReleaseTime: "2099-01-01T00:00:00Z", Patch: "private/patch.ppf"}}
	event := events.APIGatewayV2HTTPRequest{RawPath: "/v1/featured-mods"}
	event.RequestContext.HTTP.Method = "GET"
	res, err := LambdaHandler(catalog.API{Store: store})(context.Background(), event)
	var mod catalog.FeaturedMod
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("%+v %v", res, err)
	}
	if err := json.Unmarshal([]byte(res.Body), &mod); err != nil {
		t.Fatal(err)
	}
	if mod.Title != "Featured castle" || mod.DownloadAvailable || mod.Patch != "" || mod.DownloadURL != "/v1/featured-mods/"+mod.ID+"/download" {
		t.Fatalf("incorrect metadata: %+v", mod)
	}
}
