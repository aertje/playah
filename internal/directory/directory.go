// Package directory is a small client for the Radio Browser API, a free
// directory of internet radio stations.
package directory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultLimit is the number of stations that a query returns when it
	// sets no limit of its own.
	DefaultLimit = 100

	fallbackServer = "https://all.api.radio-browser.info"
	facetLimit     = "100"

	// attemptTimeout bounds one try on one server, body included. The caller
	// gives a whole request 15 seconds, so three servers must fit in that.
	attemptTimeout = 4 * time.Second

	// maxBody caps what the client reads from one answer. The largest real
	// answer is far smaller, so a bigger one means a broken server.
	maxBody = 16 << 20

	// maxDrain caps what the client reads to let a connection be reused.
	maxDrain = 64 << 10
)

// Station is one station from the directory.
type Station struct {
	UUID    string   // "stationuuid"
	Name    string   // "name", whitespace trimmed
	URL     string   // "url_resolved", falling back to "url" when empty
	Codec   string   // "codec"
	Bitrate int      // "bitrate" in kbps, 0 when unknown
	Country string   // "countrycode" (ISO 3166-1 alpha-2)
	Tags    []string // "tags" split on commas, each trimmed, empty ones dropped
}

// Kind tells which sort of value a Facet holds.
type Kind int

// The kinds of facet that the directory can list.
const (
	Genre Kind = iota
	Country
	Language
)

// Facet is one value to browse by: a tag, a country or a language.
type Facet struct {
	Kind  Kind
	Name  string // what to show: the tag, the country name, the language name
	Code  string // what to filter on: the tag, the ISO country code, the language name
	Count int    // "stationcount"
}

// Query describes one search for stations.
type Query struct {
	Name  string // part of the station name; empty matches every station
	Facet *Facet // nil for no filter
	Limit int    // 0 means DefaultLimit
}

// Client talks to the servers of the directory. The servers hold the same
// data, so the client moves on to the next one when a server fails. It is
// safe for concurrent use.
type Client struct {
	userAgent string
	http      *http.Client
	// lookupSRV finds the servers. It is a field so that a test can replace
	// the resolver and stay off the network.
	lookupSRV func(ctx context.Context, service, proto, name string) (string, []*net.SRV, error)
	// attemptTimeout is a field so that a test can shrink it.
	attemptTimeout time.Duration
	// lookup holds one token. Whoever has it may run the lookup. A channel
	// lets a waiting caller give up when its own context ends, which a mutex
	// would not.
	lookup chan struct{}

	mu       sync.Mutex
	servers  []string
	resolved bool // true once servers holds the final list
	good     int  // index in servers of the server that answered last
}

// New returns a client that finds the servers of the directory on its first
// request. The directory asks every client to pick a server from its DNS
// records, so that no single server takes all the load.
func New(userAgent string) *Client {
	c := newClient(userAgent)
	c.lookupSRV = net.DefaultResolver.LookupSRV
	return c
}

// NewWithServers returns a client that uses the given base URLs in the given
// order. A test uses it to point the client at a local server.
func NewWithServers(userAgent string, servers []string) *Client {
	c := newClient(userAgent)
	c.servers = append([]string(nil), servers...)
	c.resolved = true
	return c
}

func newClient(userAgent string) *Client {
	return &Client{
		userAgent:      userAgent,
		http:           &http.Client{},
		attemptTimeout: attemptTimeout,
		lookup:         make(chan struct{}, 1),
	}
}

// rawStation is the shape of a station in the answer of the directory.
type rawStation struct {
	UUID        string `json:"stationuuid"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	URLResolved string `json:"url_resolved"`
	Tags        string `json:"tags"`
	CountryCode string `json:"countrycode"`
	Codec       string `json:"codec"`
	Bitrate     int    `json:"bitrate"`
}

// Stations searches the directory. The directory sorts the stations by
// popularity, and so does the result. Many stations appear more than once
// under the same stream, so only the most popular one of each stream stays.
func (c *Client) Stations(ctx context.Context, q Query) ([]Station, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	v := url.Values{}
	v.Set("hidebroken", "true")
	v.Set("order", "clickcount")
	v.Set("reverse", "true")
	v.Set("limit", strconv.Itoa(limit))
	if name := strings.TrimSpace(q.Name); name != "" {
		v.Set("name", name)
	}
	if f := q.Facet; f != nil {
		switch f.Kind {
		case Genre:
			v.Set("tag", f.Code)
			v.Set("tagExact", "true")
		case Country:
			v.Set("countrycode", f.Code)
		case Language:
			v.Set("language", f.Code)
			v.Set("languageExact", "true")
		}
	}

	var raw []rawStation
	if err := c.get(ctx, "/json/stations/search", v, &raw); err != nil {
		return nil, err
	}

	out := make([]Station, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, r := range raw {
		u, ok := streamURL(r)
		if !ok || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, Station{
			UUID:    r.UUID,
			Name:    strings.TrimSpace(r.Name),
			URL:     u,
			Codec:   r.Codec,
			Bitrate: r.Bitrate,
			Country: r.CountryCode,
			Tags:    splitTags(r.Tags),
		})
	}
	return out, nil
}

// streamURL picks the stream address of a station. Anyone can edit the
// directory and playah hands the address to mpv, which opens files and many
// other schemes, so only an http or https address with a host passes. When
// url_resolved is unusable but url is fine, url is used.
func streamURL(r rawStation) (string, bool) {
	for _, s := range []string{r.URLResolved, r.URL} {
		s = strings.TrimSpace(s)
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			continue
		}
		if sc := strings.ToLower(u.Scheme); sc == "http" || sc == "https" {
			return s, true
		}
	}
	return "", false
}

// splitTags turns the comma separated tags of the directory into a list.
func splitTags(s string) []string {
	var tags []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

// rawFacet is the shape of a tag, a country or a language in the answer of
// the directory. The three lists share one shape with different code fields.
type rawFacet struct {
	Name         string `json:"name"`
	ISO3166      string `json:"iso_3166_1"`
	StationCount int    `json:"stationcount"`
}

// Facets lists the values that the stations can be browsed by, the biggest
// first. The directory sorts them, and the result keeps that order.
func (c *Client) Facets(ctx context.Context, k Kind) ([]Facet, error) {
	v := url.Values{}
	v.Set("order", "stationcount")
	v.Set("reverse", "true")
	v.Set("hidebroken", "true")
	var path string
	switch k {
	case Genre:
		path = "/json/tags"
		v.Set("limit", facetLimit)
	case Country:
		path = "/json/countries"
	case Language:
		path = "/json/languages"
		v.Set("limit", facetLimit)
	default:
		return nil, fmt.Errorf("unknown kind of facet %d", int(k))
	}

	var raw []rawFacet
	if err := c.get(ctx, path, v, &raw); err != nil {
		return nil, err
	}

	out := make([]Facet, 0, len(raw))
	for _, r := range raw {
		name := strings.TrimSpace(r.Name)
		if name == "" || r.StationCount <= 0 {
			continue
		}
		f := Facet{Kind: k, Name: name, Code: name, Count: r.StationCount}
		if k == Country {
			f.Code = strings.TrimSpace(r.ISO3166)
			if f.Code == "" {
				continue
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// Click tells the directory that a station played. The directory counts the
// play for its popularity ranking, which keeps the order of the search
// useful for everyone. A station without an id costs no request.
func (c *Client) Click(ctx context.Context, uuid string) error {
	if uuid == "" {
		return nil
	}
	return c.get(ctx, "/json/url/"+url.PathEscape(uuid), nil, nil)
}

// directoryError is an error that reads well in a status line. The cause
// stays reachable for a caller that wants the detail.
type directoryError struct {
	msg   string
	cause error
}

func (e *directoryError) Error() string { return e.msg }
func (e *directoryError) Unwrap() error { return e.cause }

// get sends one GET request and decodes the JSON answer into out. A nil out
// ignores the body. It tries the server that worked last first, and it moves
// on to the next one after a network error or a server error. A client error
// or an unreadable answer would repeat on every server, so those stop at once.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	servers, start, err := c.order(ctx)
	if err != nil {
		return err
	}

	var last error
	for i := range servers {
		idx := (start + i) % len(servers)
		err := c.attempt(ctx, servers[idx]+path, query, out)
		var fatal *fatalError
		switch {
		case err == nil:
			c.remember(idx)
			return nil
		case ctx.Err() != nil:
			// The caller's deadline or cancel, not a slow server.
			return stopped(ctx)
		case errors.As(err, &fatal):
			return fatal.err
		}
		last = err
	}
	return &directoryError{"the station directory did not answer", last}
}

// stopped gives the error of a request whose context ended. A deadline
// means a server that answered too slowly, which the user can act on, and
// a cancel means that the caller no longer wants the answer.
func stopped(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &directoryError{"the station directory did not answer in time", ctx.Err()}
	}
	return &directoryError{"the request was cancelled", ctx.Err()}
}

// fatalError marks a failure that no other server can fix.
type fatalError struct{ err error }

func (e *fatalError) Error() string { return e.err.Error() }

// attempt tries one server. The time limit covers the answer and the body, so
// one hung server cannot use up the time that the others need. The context of
// the caller stays the outer bound.
func (c *Client) attempt(ctx context.Context, rawURL string, query url.Values, out any) error {
	actx, cancel := context.WithTimeout(ctx, c.attemptTimeout)
	defer cancel()
	resp, err := c.do(actx, rawURL, query)
	if err != nil {
		return err
	}
	return c.finish(actx, resp, out)
}

// do sends the request to one server.
func (c *Client) do(ctx context.Context, rawURL string, query url.Values) (*http.Response, error) {
	if len(query) > 0 {
		rawURL += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	return c.http.Do(req)
}

// finish reads one answer and closes it. It returns a fatalError when the
// next server would not do better, and a plain error when it would.
func (c *Client) finish(ctx context.Context, resp *http.Response, out any) error {
	// A body that is not read to its end stops the connection from being
	// reused, so a bounded rest is read first. A fault on close tells
	// nothing that the answer did not already tell.
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrain))
		_ = resp.Body.Close()
	}()
	switch {
	case resp.StatusCode >= 500:
		return fmt.Errorf("server answered %s", resp.Status)
	case resp.StatusCode >= 400:
		return &fatalError{&directoryError{
			"the station directory refused the request",
			fmt.Errorf("server answered %s", resp.Status),
		}}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(out); err != nil {
		var netErr net.Error
		if ctx.Err() != nil || errors.As(err, &netErr) {
			// The connection broke while the body came in.
			return err
		}
		return &fatalError{&directoryError{
			"the station directory sent an answer that playah cannot read", err,
		}}
	}
	return nil
}

// order returns the servers and the index to start from. It finds the
// servers first when the client has not done so yet.
func (c *Client) order(ctx context.Context) ([]string, int, error) {
	if err := c.resolve(ctx); err != nil {
		return nil, 0, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.servers) == 0 {
		return nil, 0, &directoryError{"no station directory is set up", nil}
	}
	return c.servers, c.good, nil
}

// resolve runs the lookup once. A caller that finds another one busy with
// the lookup waits for it, and gives up when its own context ends. When the
// lookup fails for the busy caller, the next waiter tries again.
func (c *Client) resolve(ctx context.Context) error {
	if c.isResolved() {
		return nil
	}
	if ctx.Err() != nil {
		return stopped(ctx)
	}
	select {
	case c.lookup <- struct{}{}:
	case <-ctx.Done():
		return stopped(ctx)
	}
	defer func() { <-c.lookup }()
	if c.isResolved() {
		return nil
	}
	servers, err := c.discover(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.servers = servers
	c.resolved = true
	c.mu.Unlock()
	return nil
}

func (c *Client) isResolved() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resolved
}

// discover looks the servers up in DNS. A failed or empty lookup gives the
// shared address of the directory, which always works but leans on one name.
// A lookup that the caller cancelled says nothing about DNS, so it returns
// an error and the next request tries again.
func (c *Client) discover(ctx context.Context) ([]string, error) {
	_, records, err := c.lookupSRV(ctx, "api", "tcp", "radio-browser.info")
	if err != nil && ctx.Err() != nil {
		return nil, stopped(ctx)
	}
	var servers []string
	for _, r := range records {
		host := strings.TrimSuffix(r.Target, ".")
		if host != "" {
			servers = append(servers, "https://"+host)
		}
	}
	if len(servers) == 0 {
		return []string{fallbackServer}, nil
	}
	rand.Shuffle(len(servers), func(i, j int) {
		servers[i], servers[j] = servers[j], servers[i]
	})
	return servers, nil
}

// remember keeps the server that answered, so the next request starts there.
func (c *Client) remember(idx int) {
	c.mu.Lock()
	c.good = idx
	c.mu.Unlock()
}
