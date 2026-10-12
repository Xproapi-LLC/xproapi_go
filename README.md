# xproapi (Go)

Go client for the [Xproapi](https://xproapi.com/docs) X (Twitter) data API. Go 1.21+, standard library only.

## Install

```bash
go get github.com/Xproapi-LLC/xproapi_go
```

## Usage

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	xproapi "github.com/Xproapi-LLC/xproapi_go"
)

func main() {
	x, err := xproapi.New(os.Getenv("XPROAPI_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	response, err := x.UserInfo(ctx, "jack")
	if err != nil {
		log.Fatal(err)
	}
	user := response["data"].(map[string]any)
	fmt.Printf("%s has %.0f followers\n", user["name"], user["followers"])

	// One page (up to 200), then the next
	page, _ := x.Followers(ctx, "jack", xproapi.Params{"pageSize": 200})
	next, _ := x.Followers(ctx, "jack", xproapi.Params{"cursor": page["next_cursor"]})
	_ = next

	// Every page, capped at 5 requests
	it := x.Paginate(ctx, "followers", xproapi.Params{"userName": "jack"}, 5)
	for it.Next() {
		fmt.Println(it.Item()["userName"])
	}
	if err := it.Err(); err != nil {
		log.Fatal(err)
	}

	// Tweets
	x.LastTweets(ctx, xproapi.Params{"userName": "jack", "includeReplies": false})
	x.AdvancedSearch(ctx, "from:jack min_faves:100", xproapi.Params{"queryType": "Top"})
	x.TweetsByIDs(ctx, []string{"20", "1800000000000000000"})
	x.Replies(ctx, "https://x.com/jack/status/20", nil) // ids or links
}
```

Methods return the API's JSON as `map[string]any` (numbers are `float64`, lists `[]any`). Optional parameters go in `xproapi.Params` under the API's own names (`cursor`, `pageSize`, `queryType`…); pass `nil` for none.

### Options

```go
x, err := xproapi.New("xp_...",
	xproapi.WithBaseURL("https://xproapi.com/api"), // default
	xproapi.WithTimeout(30*time.Second),            // per request
	xproapi.WithMaxRetries(2),                      // for 429 / 502 / 503 / network errors; 0 = off
	xproapi.WithHTTPClient(http.DefaultClient),     // your own client (proxies, tests)
)
```

Every call takes a `context.Context`; cancelling it stops the request and any retries.

### Errors

```go
_, err := x.UserInfo(ctx, "someone")

var apiErr *xproapi.Error

switch {
case err == nil:
case errors.Is(err, xproapi.ErrNotFound):
	// no such user
case errors.Is(err, xproapi.ErrInsufficientCredits):
	// top up; nothing was charged
case errors.Is(err, xproapi.ErrRateLimit) && errors.As(err, &apiErr):
	time.Sleep(time.Duration(apiErr.RetryAfter) * time.Second)
case errors.As(err, &apiErr):
	log.Println(apiErr.Status, apiErr.Message)
}
```

The sentinels are `ErrBadRequest` (400), `ErrAuthentication` (401), `ErrInsufficientCredits` (402), `ErrPermissionDenied` (403), `ErrNotFound` (404), `ErrRateLimit` (429) and `ErrServiceUnavailable` (5xx). Network failures are `*xproapi.Error` values with `Status` 0.

### Rate limit

```go
x.RateLimit() // {Limit: 60, Remaining: 57} from the last response (-1 before the first)
```

### Anything else

`x.Get(ctx, "/twitter/user/info", xproapi.Params{"userName": "jack"})` calls any GET endpoint with the same auth, retries and errors.

## Tests

```bash
go test ./...
```
