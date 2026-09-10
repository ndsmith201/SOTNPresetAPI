package transport

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	"sotnpresetapi/internal/catalog"
)

type captureStore struct {
	catalog.Store
	item     catalog.Item
	existing *catalog.Item
}

func (s *captureStore) List(context.Context, string, int, string) (catalog.Page, error) {
	if s.existing != nil {
		return catalog.Page{Items: []catalog.Item{*s.existing}}, nil
	}
	return catalog.Page{}, nil
}
func (s *captureStore) SavePreset(_ context.Context, item catalog.Item, previous *catalog.Item) (catalog.Item, error) {
	if previous != nil {
		data := item.Data
		item = *previous
		item.Data = data
	}
	s.item = item
	return item, nil
}

func (s *captureStore) Create(_ context.Context, item catalog.Item) error {
	s.item = item
	return nil
}

func TestLambdaUsesVerifiedSubject(t *testing.T) {
	store := &captureStore{}
	handler := LambdaHandler(catalog.API{Store: store})
	event := events.APIGatewayV2HTTPRequest{
		RawPath: "/v1/presets",
		Headers: map[string]string{"content-type": "application/json", "X-Dev-User": "spoofed"},
		Body:    `{"metadata":{"id":"sample","name":"Sample"}}`,
	}
	event.RequestContext.HTTP.Method = "POST"
	event.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{
		JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{
			Claims: map[string]string{"sub": "verified-user", "token_use": "access"},
		},
	}
	res, err := handler(context.Background(), event)
	if err != nil || res.StatusCode != 201 || store.item.CreatedBy != "verified-user" {
		t.Fatalf("unexpected response: %+v %v; item: %+v", res, err, store.item)
	}
	var item catalog.Item
	if err := json.Unmarshal([]byte(res.Body), &item); err != nil || item.ID != store.item.ID {
		t.Fatalf("invalid response body: %s", res.Body)
	}
}

func TestLambdaRejectsSpoofedIdentity(t *testing.T) {
	handler := LambdaHandler(catalog.API{})
	event := events.APIGatewayV2HTTPRequest{RawPath: "/v1/options", Headers: map[string]string{"Content-Type": "application/json", "X-Dev-User": "alice", "Authorization": "Bearer fake"}, Body: `{}`}
	event.RequestContext.HTTP.Method = "POST"
	res, err := handler(context.Background(), event)
	if err != nil || res.StatusCode != 401 {
		t.Fatalf("%+v %v", res, err)
	}
	event.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{Claims: map[string]string{"sub": "alice", "token_use": "id"}}}
	res, _ = handler(context.Background(), event)
	if res.StatusCode != 401 {
		t.Fatal("accepted ID token")
	}
	event.RequestContext.Authorizer.JWT.Claims["token_use"] = "access"
	event.IsBase64Encoded = true
	event.Body = base64.StdEncoding.EncodeToString([]byte("{}"))
	res, _ = handler(context.Background(), event)
	if res.StatusCode != 400 {
		t.Fatalf("authenticated request did not reach validation: %+v", res)
	}
	event.Body = "%%%"
	res, _ = handler(context.Background(), event)
	if res.StatusCode != 400 {
		t.Fatal(res)
	}
}

func TestPresetUpdateUsesVerifiedUsername(t *testing.T) {
	for _, username := range []string{"alice", "mallory", ""} {
		store := &captureStore{existing: &catalog.Item{ID: "0123456789abcdef0123456789abcdef", Kind: "presets", Data: json.RawMessage(`{"metadata":{"name":"Sample","author":["alice"]}}`)}}
		event := events.APIGatewayV2HTTPRequest{RawPath: "/v1/presets",
			Headers: map[string]string{"content-type": "application/json", "X-Dev-User": "alice", "username": "alice"},
			Body:    `{"metadata":{"id":"sample","name":"Sample","author":["alice","mallory"]}}`}
		event.RequestContext.HTTP.Method = "POST"
		event.RequestContext.Authorizer = &events.APIGatewayV2HTTPRequestContextAuthorizerDescription{JWT: &events.APIGatewayV2HTTPRequestContextAuthorizerJWTDescription{Claims: map[string]string{"sub": "verified-sub", "token_use": "access", "username": username}}}
		res, err := LambdaHandler(catalog.API{Store: store})(context.Background(), event)
		want := 403
		if username == "alice" {
			want = 200
		}
		if err != nil || res.StatusCode != want {
			t.Fatalf("username %q: %+v %v", username, res, err)
		}
	}
}
