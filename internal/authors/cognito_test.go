package authors

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider/types"
)

const sub = "12345678-1234-1234-1234-123456789012"

type fakeClient struct {
	calls  int
	lookup func(*cognitoidentityprovider.ListUsersInput) (*cognitoidentityprovider.ListUsersOutput, error)
}

func (f *fakeClient) ListUsers(_ context.Context, in *cognitoidentityprovider.ListUsersInput, _ ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.ListUsersOutput, error) {
	f.calls++
	return f.lookup(in)
}

func TestUsernameLookupPaginationAndCache(t *testing.T) {
	f := &fakeClient{}
	f.lookup = func(in *cognitoidentityprovider.ListUsersInput) (*cognitoidentityprovider.ListUsersOutput, error) {
		if aws.ToString(in.UserPoolId) != "test-pool" || aws.ToString(in.Filter) != `sub = "`+sub+`"` || len(in.AttributesToGet) != 1 || in.AttributesToGet[0] != "sub" {
			t.Fatalf("unexpected lookup: %+v", in)
		}
		if in.PaginationToken == nil {
			return &cognitoidentityprovider.ListUsersOutput{PaginationToken: aws.String("next")}, nil
		}
		return &cognitoidentityprovider.ListUsersOutput{Users: []types.UserType{{Username: aws.String("runner"), Attributes: []types.AttributeType{{Name: aws.String("sub"), Value: aws.String(sub)}}}}}, nil
	}
	c := &Cognito{Client: f, PoolID: "test-pool"}
	for range 2 {
		name, err := c.Username(context.Background(), sub)
		if err != nil || name != "runner" {
			t.Fatalf("%q %v", name, err)
		}
	}
	if f.calls != 2 {
		t.Fatalf("cache missed: %d calls", f.calls)
	}
	c.cache[sub] = cachedName{name: "old", expires: time.Now().Add(-time.Second)}
	if name, _ := c.Username(context.Background(), sub); name != "runner" || f.calls != 4 {
		t.Fatal("expired entry was reused")
	}
}

func TestUnknownAndFailedLookups(t *testing.T) {
	f := &fakeClient{lookup: func(*cognitoidentityprovider.ListUsersInput) (*cognitoidentityprovider.ListUsersOutput, error) {
		return &cognitoidentityprovider.ListUsersOutput{}, nil
	}}
	c := &Cognito{Client: f, PoolID: "pool"}
	for _, subject := range []string{"", "local-user", `bad"filter`} {
		if name, err := c.Username(context.Background(), subject); name != "" || err != nil {
			t.Fatal(name, err)
		}
	}
	if f.calls != 0 {
		t.Fatal("queried invalid subject")
	}
	for range 2 {
		if name, err := c.Username(context.Background(), sub); name != "" || err != nil {
			t.Fatal(name, err)
		}
	}
	if f.calls != 1 {
		t.Fatal("missing users should be briefly cached")
	}
	c.cache = nil
	f.lookup = func(*cognitoidentityprovider.ListUsersInput) (*cognitoidentityprovider.ListUsersOutput, error) {
		return nil, errors.New("unavailable")
	}
	for range 2 {
		if _, err := c.Username(context.Background(), sub); err == nil {
			t.Fatal("expected lookup failure")
		}
	}
	if f.calls != 3 {
		t.Fatal("transient errors must not be cached")
	}
}
