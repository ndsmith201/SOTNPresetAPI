package transport

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"sotnpresetapi/internal/catalog"
)

func LambdaHandler(api catalog.API) func(context.Context, events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	return func(ctx context.Context, event events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
		subject := ""
		username := ""
		// Trust only the JWT authorizer context supplied by API Gateway. No
		// caller-provided user ID or unverified Authorization header is used.
		if auth := event.RequestContext.Authorizer; auth != nil && auth.JWT != nil && auth.JWT.Claims["token_use"] == "access" {
			subject = auth.JWT.Claims["sub"]
			username = auth.JWT.Claims["username"]
		}
		body := []byte(event.Body)
		if event.IsBase64Encoded {
			decoded, err := base64.StdEncoding.DecodeString(event.Body)
			if err != nil {
				return events.APIGatewayV2HTTPResponse{StatusCode: 400, Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"error":{"code":"invalid_request","message":"invalid base64 body"}}`}, nil
			}
			body = decoded
		}
		contentType := ""
		for k, v := range event.Headers {
			if strings.EqualFold(k, "Content-Type") {
				contentType = v
			}
		}
		result := api.Handle(ctx, catalog.Request{Method: event.RequestContext.HTTP.Method, Path: event.RawPath, ContentType: contentType, Subject: subject, Username: username, Query: event.QueryStringParameters, Body: body})
		return events.APIGatewayV2HTTPResponse{StatusCode: result.Status, Headers: result.Headers, Body: result.Body}, nil
	}
}
