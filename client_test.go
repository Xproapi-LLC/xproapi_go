// Offline tests for the Go client: a fake HTTP client stands in for the API. Run: go test ./...
package xproapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

type reply struct {
	status  int
	body    any
	headers map[string]string
}

// fakeAPI answers from replies in order and records each request.
type fakeAPI struct {
	replies  []reply
	requests []*http.Request
	slept    []time.Duration
}

func (f *fakeAPI) Do(request *http.Request) (*http.Response, error) {
	f.requests = append(f.requests, request)
	next := f.replies[0]
	f.replies = f.replies[1:]

	body, _ := json.Marshal(next.body)
	header := http.Header{}
	for name, value := range next.headers {
		header.Set(name, value)
	}

	return &http.Response{StatusCode: next.status, Header: header, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
}

func fake(t *testing.T, maxRetries int, replies ...reply) (*Client, *fakeAPI) {
	t.Helper()

	api := &fakeAPI{replies: replies}
	client, err := New("xp_test_key", WithBaseURL("https://api.test/api/"), WithMaxRetries(maxRetries), WithHTTPClient(api))
	if err != nil {
		t.Fatal(err)
	}

	client.sleep = func(_ context.Context, wait time.Duration) error {
		api.slept = append(api.slept, wait)

		return nil
	}

	return client, api
}

func apiError(status int, message string, headers map[string]string) reply {
	return reply{status, map[string]any{"status": "error", "error": status, "message": message}, headers}
}

func TestSendsTheKeyAndBuildsTheQueryLeavingOutEmptyParameters(t *testing.T) {
	client, api := fake(t, 2, reply{200, map[string]any{"followers": []any{}, "has_next_page": false, "next_cursor": ""}, nil})

	if _, err := client.Followers(context.Background(), "jack", Params{"pageSize": 50, "cursor": nil}); err != nil {
		t.Fatal(err)
	}

	request := api.requests[0]
	if got := request.URL.String(); got != "https://api.test/api/twitter/user/followers?pageSize=50&userName=jack" {
		t.Errorf("url = %s", got)
	}
	if got := request.Header.Get("X-API-Key"); got != "xp_test_key" {
		t.Errorf("X-API-Key = %q", got)
	}
}

func TestSendsBooleansAsTrueFalseAndJoinsIDLists(t *testing.T) {
	client, api := fake(t, 2, reply{200, map[string]any{"tweets": []any{}}, nil}, reply{200, map[string]any{"tweets": []any{}}, nil})
	ctx := context.Background()

	client.Quotes(ctx, "123", Params{"includeReplies": false})
	client.TweetsByIDs(ctx, []string{"1", "2", "3"})

	if got := api.requests[0].URL.Query().Get("includeReplies"); got != "false" {
		t.Errorf("includeReplies = %q", got)
	}
	if got := api.requests[1].URL.RawQuery; got != "tweet_ids=1%2C2%2C3" {
		t.Errorf("query = %q", got)
	}
}

func TestAppliesDefaultQueryTypesThatOptionsCanReplace(t *testing.T) {
	client, api := fake(t, 2, reply{200, map[string]any{"tweets": []any{}}, nil}, reply{200, map[string]any{"tweets": []any{}}, nil})
	ctx := context.Background()

	client.AdvancedSearch(ctx, "from:jack", nil)
	client.RepliesV2(ctx, "123", Params{"queryType": "Likes"})

	if got := api.requests[0].URL.Query().Get("queryType"); got != "Latest" {
		t.Errorf("advanced search queryType = %q", got)
	}
	if got := api.requests[1].URL.Query().Get("queryType"); got != "Likes" {
		t.Errorf("replies v2 queryType = %q", got)
	}
}

func TestReturnsTheDecodedBodyAndRemembersTheRateLimitHeaders(t *testing.T) {
	client, _ := fake(t, 2, reply{200, map[string]any{"data": map[string]any{"userName": "jack"}}, map[string]string{"X-RateLimit-Limit": "60", "X-RateLimit-Remaining": "59"}})

	if got := client.RateLimit(); got != (RateLimit{-1, -1}) {
		t.Errorf("rate limit before the first response = %+v", got)
	}

	response, err := client.UserInfo(context.Background(), "jack")
	if err != nil {
		t.Fatal(err)
	}

	if got := response["data"].(map[string]any)["userName"]; got != "jack" {
		t.Errorf("userName = %v", got)
	}
	if got := client.RateLimit(); got != (RateLimit{60, 59}) {
		t.Errorf("rate limit = %+v", got)
	}
}

func TestMapsErrorStatusesToSentinelsWithTheAPIMessage(t *testing.T) {
	for status, kind := range map[int]error{401: ErrAuthentication, 402: ErrInsufficientCredits, 404: ErrNotFound} {
		client, _ := fake(t, 2, apiError(status, "Nope", nil))

		_, err := client.UserInfo(context.Background(), "jack")

		var apiErr *Error
		if !errors.As(err, &apiErr) || !errors.Is(err, kind) || apiErr.Status != status || apiErr.Message != "Nope" || err.Error() != "Nope" {
			t.Errorf("status %d: got %#v", status, err)
		}
		if errors.Is(err, ErrBadRequest) {
			t.Errorf("status %d also matched ErrBadRequest", status)
		}
	}
}

func TestRetriesA429AfterRetryAfterThenSucceeds(t *testing.T) {
	client, api := fake(t, 2, apiError(429, "Slow down", map[string]string{"Retry-After": "3"}), reply{200, map[string]any{"data": map[string]any{"userName": "jack"}}, nil})

	if _, err := client.UserInfo(context.Background(), "jack"); err != nil {
		t.Fatal(err)
	}

	if len(api.requests) != 2 || !reflect.DeepEqual(api.slept, []time.Duration{3 * time.Second}) {
		t.Errorf("requests = %d, slept = %v", len(api.requests), api.slept)
	}
}

func TestGivesUpAfterMaxRetriesWithARateLimitErrorCarryingRetryAfter(t *testing.T) {
	headers := map[string]string{"Retry-After": "5"}
	client, api := fake(t, 1, apiError(429, "Slow down", headers), apiError(429, "Slow down", headers))

	_, err := client.UserInfo(context.Background(), "jack")

	var apiErr *Error
	if !errors.As(err, &apiErr) || !errors.Is(err, ErrRateLimit) || apiErr.RetryAfter != 5 {
		t.Errorf("got %#v", err)
	}
	if len(api.requests) != 2 {
		t.Errorf("requests = %d", len(api.requests))
	}
}

func TestDoesNotRetryClientErrors(t *testing.T) {
	client, api := fake(t, 2, apiError(400, "userName is required.", nil))

	if _, err := client.Get(context.Background(), "/twitter/user/info", nil); !errors.Is(err, ErrBadRequest) {
		t.Errorf("got %#v", err)
	}
	if len(api.requests) != 1 {
		t.Errorf("requests = %d", len(api.requests))
	}
}

func TestPaginateFollowsNextCursorAcrossPagesAndStopsAtMaxPages(t *testing.T) {
	page := func(more bool, cursor string, ids ...string) reply {
		users := []any{}
		for _, id := range ids {
			users = append(users, map[string]any{"id": id})
		}

		return reply{200, map[string]any{"followers": users, "has_next_page": more, "next_cursor": cursor}, nil}
	}

	client, api := fake(t, 2, page(true, "c1", "1", "2"), page(true, "c2", "3"), page(false, "", "4"))
	var all []string
	it := client.Paginate(context.Background(), "followers", Params{"userName": "jack"}, 0)
	for it.Next() {
		all = append(all, it.Item()["id"].(string))
	}

	if it.Err() != nil || !reflect.DeepEqual(all, []string{"1", "2", "3", "4"}) {
		t.Errorf("all = %v, err = %v", all, it.Err())
	}
	if api.requests[0].URL.Query().Has("cursor") || api.requests[1].URL.Query().Get("cursor") != "c1" || api.requests[2].URL.Query().Get("cursor") != "c2" {
		t.Errorf("cursors = %q, %q, %q", api.requests[0].URL.RawQuery, api.requests[1].URL.RawQuery, api.requests[2].URL.RawQuery)
	}

	client, api = fake(t, 2, page(true, "c1", "1"), page(true, "c2", "2"))
	count := 0
	it = client.Paginate(context.Background(), "followers", Params{"userName": "jack"}, 1)
	for it.Next() {
		count++
	}

	if count != 1 || len(api.requests) != 1 {
		t.Errorf("count = %d, requests = %d", count, len(api.requests))
	}
}

func TestPaginateStopsWithTheErrorOfAFailedPage(t *testing.T) {
	client, _ := fake(t, 0, reply{200, map[string]any{"followers": []any{map[string]any{"id": "1"}}, "has_next_page": true, "next_cursor": "c1"}, nil}, apiError(402, "Not enough credits.", nil))

	count := 0
	it := client.Paginate(context.Background(), "followers", Params{"userName": "jack"}, 0)
	for it.Next() {
		count++
	}

	if count != 1 || !errors.Is(it.Err(), ErrInsufficientCredits) {
		t.Errorf("count = %d, err = %v", count, it.Err())
	}
}

func TestRejectsAnEmptyKeyAndUnknownPaginatedEndpoints(t *testing.T) {
	if _, err := New("  "); err == nil {
		t.Error("an empty key was accepted")
	}

	client, api := fake(t, 2)
	it := client.Paginate(context.Background(), "userInfo", nil, 0)

	if it.Next() || it.Err() == nil || len(api.requests) != 0 {
		t.Errorf("err = %v, requests = %d", it.Err(), len(api.requests))
	}
}

func TestACancelledContextStopsWithoutRetrying(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	client, err := New("xp_test_key", WithBaseURL("http://127.0.0.1:1"))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.UserInfo(ctx, "jack"); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v", err)
	}
}
