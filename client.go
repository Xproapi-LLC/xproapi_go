// Package xproapi is a client for the Xproapi X (Twitter) data API (https://xproapi.com/docs).
//
//	x, err := xproapi.New(os.Getenv("XPROAPI_KEY"))
//	user, err := x.UserInfo(ctx, "elonmusk")
//
//	it := x.Paginate(ctx, "followers", xproapi.Params{"userName": "elonmusk"}, 3)
//	for it.Next() {
//		fmt.Println(it.Item()["userName"])
//	}
//	if err := it.Err(); err != nil { ... }
//
// Every method returns the decoded JSON response. Errors are *Error values.
// Rate-limited (429) and temporarily unavailable (502/503) requests are retried automatically.
package xproapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultBaseURL is where the API lives.
	DefaultBaseURL = "https://xproapi.com/api"
	// Version of this client.
	Version = "1.0.0"
)

// Params are query parameters, under the API's own names (userName, pageSize, cursor…).
// Nil and empty values are left out; booleans are sent as "true" / "false".
type Params map[string]any

// RateLimit is your per-minute limit and the requests left this minute. Both are -1 before the first response.
type RateLimit struct {
	Limit     int
	Remaining int
}

// Doer sends an HTTP request. *http.Client is one.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// paginated maps an endpoint name to its path and the key of the list in the response, for Paginate.
var paginated = map[string][2]string{
	"searchUsers":    {"/twitter/user/search", "users"},
	"followers":      {"/twitter/user/followers", "followers"},
	"followings":     {"/twitter/user/followings", "followings"},
	"lastTweets":     {"/twitter/user/last_tweets", "tweets"},
	"tweetTimeline":  {"/twitter/user/tweet_timeline", "tweets"},
	"mentions":       {"/twitter/user/mentions", "tweets"},
	"advancedSearch": {"/twitter/tweet/advanced_search", "tweets"},
	"replies":        {"/twitter/tweet/replies", "replies"},
	"repliesV2":      {"/twitter/tweet/replies/v2", "tweets"},
	"quotes":         {"/twitter/tweet/quotes", "tweets"},
	"retweeters":     {"/twitter/tweet/retweeters", "users"},
	"threadContext":  {"/twitter/tweet/thread_context", "replies"},
}

// Client calls the Xproapi public API. Create one with New; it is safe for concurrent use.
type Client struct {
	apiKey     string
	baseURL    string
	timeout    time.Duration
	maxRetries int
	http       Doer
	sleep      func(context.Context, time.Duration) error

	mu        sync.Mutex
	rateLimit RateLimit
}

// Option changes a setting of a new Client.
type Option func(*Client)

// WithBaseURL replaces the default https://xproapi.com/api.
func WithBaseURL(baseURL string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(baseURL, "/") }
}

// WithTimeout sets the time allowed per request (default 30 seconds).
func WithTimeout(timeout time.Duration) Option {
	return func(c *Client) { c.timeout = timeout }
}

// WithMaxRetries sets the retries for 429 / 502 / 503 / network errors (default 2, 0 = never).
func WithMaxRetries(maxRetries int) Option {
	return func(c *Client) { c.maxRetries = maxRetries }
}

// WithHTTPClient replaces http.DefaultClient (for proxies or tests).
func WithHTTPClient(client Doer) Option {
	return func(c *Client) { c.http = client }
}

// New returns a client for the given API key (from your panel → API → API keys).
func New(apiKey string, options ...Option) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, errors.New("xproapi: an API key is required. Create one in your panel under API → API keys")
	}

	client := &Client{
		apiKey:     apiKey,
		baseURL:    DefaultBaseURL,
		timeout:    30 * time.Second,
		maxRetries: 2,
		http:       http.DefaultClient,
		sleep:      sleep,
		rateLimit:  RateLimit{Limit: -1, Remaining: -1},
	}

	for _, option := range options {
		option(client)
	}

	return client, nil
}

// ---- Users -------------------------------------------------------------------------------------

// UserInfo returns the profile of one user. Response: {data: User}.
func (c *Client) UserInfo(ctx context.Context, userName string) (map[string]any, error) {
	return c.Get(ctx, "/twitter/user/info", Params{"userName": userName})
}

// BatchUserInfoByUsernames returns the profiles of up to 100 users in one call (cheaper per profile). Response: {users: [User]}.
func (c *Client) BatchUserInfoByUsernames(ctx context.Context, userNames []string) (map[string]any, error) {
	return c.Get(ctx, "/twitter/user/batch_info_by_usernames", Params{"userNames": strings.Join(userNames, ",")})
}

// UserAbout returns "About this account": country, join date, username changes… Response: {data: {...}}.
func (c *Client) UserAbout(ctx context.Context, userName string) (map[string]any, error) {
	return c.Get(ctx, "/twitter/user_about", Params{"userName": userName})
}

// SearchUsers is the people search. Options: cursor. Response: {users: [User], has_next_page, next_cursor}.
func (c *Client) SearchUsers(ctx context.Context, query string, options Params) (map[string]any, error) {
	return c.Get(ctx, "/twitter/user/search", merge(options, Params{"query": query}))
}

// Followers returns followers, newest first. Options: cursor, pageSize (20–200, default 200).
// Response: {followers: [User], has_next_page, next_cursor}.
func (c *Client) Followers(ctx context.Context, userName string, options Params) (map[string]any, error) {
	return c.Get(ctx, "/twitter/user/followers", merge(options, Params{"userName": userName}))
}

// Followings returns the accounts the user follows, newest first. Options: cursor, pageSize.
// Response: {followings: [User], has_next_page, next_cursor}.
func (c *Client) Followings(ctx context.Context, userName string, options Params) (map[string]any, error) {
	return c.Get(ctx, "/twitter/user/followings", merge(options, Params{"userName": userName}))
}

// LastTweets returns the user's tweets and retweets, newest first.
// Params: userName or userId, includeReplies, cursor.
func (c *Client) LastTweets(ctx context.Context, params Params) (map[string]any, error) {
	return c.Get(ctx, "/twitter/user/last_tweets", params)
}

// TweetTimeline is like LastTweets; with includeReplies + includeParentTweet the tweets being replied to are kept too.
// Params: userName or userId, includeReplies, includeParentTweet, cursor.
func (c *Client) TweetTimeline(ctx context.Context, params Params) (map[string]any, error) {
	return c.Get(ctx, "/twitter/user/tweet_timeline", params)
}

// Mentions returns tweets by others that mention the user. Options: sinceTime, untilTime (unix seconds), cursor.
func (c *Client) Mentions(ctx context.Context, userName string, options Params) (map[string]any, error) {
	return c.Get(ctx, "/twitter/user/mentions", merge(options, Params{"userName": userName}))
}

// ---- Tweets ------------------------------------------------------------------------------------

// TweetsByIDs returns up to 100 tweets by id. Response: {tweets: [Tweet]}.
func (c *Client) TweetsByIDs(ctx context.Context, tweetIDs []string) (map[string]any, error) {
	return c.Get(ctx, "/twitter/tweets", Params{"tweet_ids": strings.Join(tweetIDs, ",")})
}

// AdvancedSearch is X search with its operators (from:, since:, min_faves:, lang:, filter:…).
// Options: queryType (Latest, the default, or Top), cursor.
func (c *Client) AdvancedSearch(ctx context.Context, query string, options Params) (map[string]any, error) {
	return c.Get(ctx, "/twitter/tweet/advanced_search", merge(Params{"queryType": "Latest"}, options, Params{"query": query}))
}

// Replies returns the replies in a tweet's conversation, newest first. Options: sinceTime, untilTime, cursor.
// Response: {replies: [Tweet], ...}.
func (c *Client) Replies(ctx context.Context, tweetID string, options Params) (map[string]any, error) {
	return c.Get(ctx, "/twitter/tweet/replies", merge(options, Params{"tweetId": tweetID}))
}

// RepliesV2 returns replies ranked like X shows them. Options: queryType (Relevance, the default, Latest or Likes), cursor.
// Response: {tweets: [Tweet], ...}.
func (c *Client) RepliesV2(ctx context.Context, tweetID string, options Params) (map[string]any, error) {
	return c.Get(ctx, "/twitter/tweet/replies/v2", merge(Params{"queryType": "Relevance"}, options, Params{"tweetId": tweetID}))
}

// Quotes returns the quote tweets of a tweet. Options: sinceTime, untilTime, includeReplies, cursor.
func (c *Client) Quotes(ctx context.Context, tweetID string, options Params) (map[string]any, error) {
	return c.Get(ctx, "/twitter/tweet/quotes", merge(options, Params{"tweetId": tweetID}))
}

// Retweeters returns the users who retweeted a tweet. Options: cursor. Response: {users: [User], ...}.
func (c *Client) Retweeters(ctx context.Context, tweetID string, options Params) (map[string]any, error) {
	return c.Get(ctx, "/twitter/tweet/retweeters", merge(options, Params{"tweetId": tweetID}))
}

// ThreadContext returns the whole conversation around a tweet, in reading order. Options: cursor.
// Response: {replies: [Tweet], ...}.
func (c *Client) ThreadContext(ctx context.Context, tweetID string, options Params) (map[string]any, error) {
	return c.Get(ctx, "/twitter/tweet/thread_context", merge(options, Params{"tweetId": tweetID}))
}

// Article returns the long-form article attached to a tweet. Response: {article: {...}}.
func (c *Client) Article(ctx context.Context, tweetID string) (map[string]any, error) {
	return c.Get(ctx, "/twitter/article", Params{"tweet_id": tweetID})
}

// ---- Pagination, raw requests ------------------------------------------------------------------

// RateLimit returns the rate limit of the last response.
func (c *Client) RateLimit() RateLimit {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.rateLimit
}

// Iterator walks every item of a paginated endpoint. See Client.Paginate.
type Iterator struct {
	client   *Client
	ctx      context.Context
	path     string
	key      string
	params   Params
	maxPages int

	items   []any
	index   int
	pages   int
	done    bool
	current map[string]any
	err     error
}

// Paginate returns every item of a paginated endpoint, page after page, following next_cursor.
// Each page is a separate (charged) request; stop early with maxPages (0 = no cap) or by leaving the loop.
//
// endpoint is one of: searchUsers, followers, followings, lastTweets, tweetTimeline, mentions,
// advancedSearch, replies, repliesV2, quotes, retweeters, threadContext.
// params are the endpoint's query parameters (without cursor).
func (c *Client) Paginate(ctx context.Context, endpoint string, params Params, maxPages int) *Iterator {
	entry, ok := paginated[endpoint]
	if !ok {
		names := make([]string, 0, len(paginated))
		for name := range paginated {
			names = append(names, name)
		}
		sort.Strings(names)

		return &Iterator{done: true, err: fmt.Errorf("xproapi: %s isn't paginated. Use one of: %s", endpoint, strings.Join(names, ", "))}
	}

	return &Iterator{client: c, ctx: ctx, path: entry[0], key: entry[1], params: params, maxPages: maxPages}
}

// Next moves to the next item, fetching the next page when needed. It returns false at the end or on an error (see Err).
func (it *Iterator) Next() bool {
	for {
		if it.index < len(it.items) {
			it.current, _ = it.items[it.index].(map[string]any)
			it.index++

			return true
		}

		if it.done {
			return false
		}

		page, err := it.client.Get(it.ctx, it.path, it.params)
		if err != nil {
			it.err, it.done = err, true

			return false
		}

		it.pages++
		it.items, _ = page[it.key].([]any)
		it.index = 0

		cursor := ""
		if more, _ := page["has_next_page"].(bool); more && page["next_cursor"] != nil {
			cursor = fmt.Sprint(page["next_cursor"])
		}

		if cursor == "" || (it.maxPages > 0 && it.pages >= it.maxPages) {
			it.done = true
		} else {
			it.params = merge(it.params, Params{"cursor": cursor})
		}
	}
}

// Item is the current item (a user or a tweet).
func (it *Iterator) Item() map[string]any { return it.current }

// Err is the error that stopped the iteration, if any.
func (it *Iterator) Err() error { return it.err }

// Get calls any GET endpoint under the base URL, e.g. Get(ctx, "/twitter/user/info", Params{"userName": "jack"}).
func (c *Client) Get(ctx context.Context, path string, params Params) (map[string]any, error) {
	query := url.Values{}
	for name, value := range params {
		if text, ok := format(value); ok {
			query.Set(name, text)
		}
	}

	address := c.baseURL + "/" + strings.TrimLeft(path, "/")
	if len(query) > 0 {
		address += "?" + query.Encode()
	}

	for attempt := 0; ; attempt++ {
		status, header, body, err := c.send(ctx, address)
		if err != nil {
			// The caller gave up: don't retry, and report their context's error.
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			if attempt < c.maxRetries {
				if err := c.sleep(ctx, backoff(attempt, 0)); err != nil {
					return nil, err
				}

				continue
			}

			return nil, &Error{Message: "Network error: " + err.Error(), Err: err}
		}

		c.mu.Lock()
		c.rateLimit = RateLimit{Limit: intHeader(header, "X-RateLimit-Limit", -1), Remaining: intHeader(header, "X-RateLimit-Remaining", -1)}
		c.mu.Unlock()

		var decoded map[string]any
		if json.Unmarshal(body, &decoded) != nil {
			decoded = nil
		}

		if status >= 200 && status < 300 && decoded != nil {
			return decoded, nil
		}

		retryAfter := intHeader(header, "Retry-After", 0)

		if (status == 429 || status == 502 || status == 503) && attempt < c.maxRetries {
			if err := c.sleep(ctx, backoff(attempt, retryAfter)); err != nil {
				return nil, err
			}

			continue
		}

		return nil, errorFromResponse(status, decoded, retryAfter)
	}
}

func (c *Client) send(ctx context.Context, address string) (int, http.Header, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return 0, nil, nil, err
	}

	request.Header.Set("X-API-Key", c.apiKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "xproapi-go/"+Version)

	response, err := c.http.Do(request)
	if err != nil {
		return 0, nil, nil, err
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, nil, nil, err
	}

	return response.StatusCode, response.Header, body, nil
}

// backoff is the wait before a retry: Retry-After when sent (capped at 60s), else 1s, 2s, 4s…
func backoff(attempt int, retryAfter int) time.Duration {
	if retryAfter > 0 {
		return time.Duration(min(60, retryAfter)) * time.Second
	}

	return time.Duration(1<<attempt) * time.Second
}

func sleep(ctx context.Context, wait time.Duration) error {
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func intHeader(header http.Header, name string, missing int) int {
	value, err := strconv.Atoi(strings.TrimSpace(header.Get(name)))
	if err != nil {
		return missing
	}

	return value
}

// format turns a parameter into its query string form; false means "leave it out".
func format(value any) (string, bool) {
	switch v := value.(type) {
	case nil:
		return "", false
	case string:
		return v, v != ""
	case bool:
		return strconv.FormatBool(v), true
	default:
		return fmt.Sprint(v), true
	}
}

// merge returns the given params as one map; later ones win.
func merge(all ...Params) Params {
	merged := Params{}
	for _, params := range all {
		for name, value := range params {
			merged[name] = value
		}
	}

	return merged
}
