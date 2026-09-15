package authors

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cognitoidentityprovider"
)

type Client interface {
	ListUsers(context.Context, *cognitoidentityprovider.ListUsersInput, ...func(*cognitoidentityprovider.Options)) (*cognitoidentityprovider.ListUsersOutput, error)
}

type cachedName struct {
	name    string
	expires time.Time
}

type Cognito struct {
	Client Client
	PoolID string
	mu     sync.Mutex
	cache  map[string]cachedName
}

var subjectID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func (c *Cognito) Username(ctx context.Context, subject string) (string, error) {
	if !subjectID.MatchString(subject) {
		return "", nil
	}
	c.mu.Lock()
	cached, ok := c.cache[subject]
	c.mu.Unlock()
	if ok && time.Now().Before(cached.expires) {
		return cached.name, nil
	}
	in := &cognitoidentityprovider.ListUsersInput{
		UserPoolId: aws.String(c.PoolID), Filter: aws.String(fmt.Sprintf(`sub = "%s"`, subject)),
		AttributesToGet: []string{"sub"}, Limit: aws.Int32(1),
	}
	name := ""
	for {
		out, err := c.Client.ListUsers(ctx, in)
		if err != nil {
			return "", err
		}
		for _, user := range out.Users {
			for _, attribute := range user.Attributes {
				if aws.ToString(attribute.Name) == "sub" && aws.ToString(attribute.Value) == subject {
					name = strings.TrimSpace(aws.ToString(user.Username))
				}
			}
		}
		if name != "" || aws.ToString(out.PaginationToken) == "" {
			break
		}
		if aws.ToString(in.PaginationToken) == aws.ToString(out.PaginationToken) {
			return "", fmt.Errorf("Cognito returned a repeated pagination token")
		}
		in.PaginationToken = out.PaginationToken
	}
	ttl := 15 * time.Minute
	if name == "" {
		ttl = time.Minute
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cache == nil || len(c.cache) >= 1000 {
		c.cache = make(map[string]cachedName)
	}
	c.cache[subject] = cachedName{name: name, expires: time.Now().Add(ttl)}
	return name, nil
}
