package directory

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testAgent = "playah-test/1.0"

// recorder is a test server that stores the requests it gets and answers
// with a fixed status and body.
type recorder struct {
	*httptest.Server
	mu     sync.Mutex
	paths  []string
	agents []string
	query  []url.Values
}

// newRecorder starts a server that answers every request with status and body.
func newRecorder(t *testing.T, status int, body string) *recorder {
	t.Helper()
	r := &recorder{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.paths = append(r.paths, req.URL.EscapedPath())
		r.agents = append(r.agents, req.Header.Get("User-Agent"))
		r.query = append(r.query, req.URL.Query())
		r.mu.Unlock()
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(r.Close)
	return r
}

// hits returns the number of requests that the server got.
func (r *recorder) hits() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.paths)
}

func TestStationsQuery(t *testing.T) {
	tests := []struct {
		name string
		q    Query
		want url.Values
	}{
		{
			name: "empty query",
			q:    Query{},
			want: url.Values{"limit": {"100"}},
		},
		{
			name: "name is trimmed",
			q:    Query{Name: "  groove  ", Limit: 5},
			want: url.Values{"limit": {"5"}, "name": {"groove"}},
		},
		{
			name: "blank name is dropped",
			q:    Query{Name: "   "},
			want: url.Values{"limit": {"100"}},
		},
		{
			name: "genre",
			q:    Query{Facet: &Facet{Kind: Genre, Code: "drum & bass"}},
			want: url.Values{"limit": {"100"}, "tag": {"drum & bass"}, "tagExact": {"true"}},
		},
		{
			name: "country",
			q:    Query{Facet: &Facet{Kind: Country, Code: "DE"}},
			want: url.Values{"limit": {"100"}, "countrycode": {"DE"}},
		},
		{
			name: "language",
			q:    Query{Name: "x", Facet: &Facet{Kind: Language, Code: "english"}, Limit: 7},
			want: url.Values{"limit": {"7"}, "name": {"x"}, "language": {"english"}, "languageExact": {"true"}},
		},
		{
			name: "negative limit takes the default",
			q:    Query{Limit: -3},
			want: url.Values{"limit": {"100"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newRecorder(t, 200, `[]`)
			c := NewWithServers(testAgent, []string{srv.URL})
			if _, err := c.Stations(context.Background(), tt.q); err != nil {
				t.Fatal(err)
			}
			if srv.paths[0] != "/json/stations/search" {
				t.Errorf("path = %q", srv.paths[0])
			}
			want := url.Values{"hidebroken": {"true"}, "order": {"clickcount"}, "reverse": {"true"}}
			for k, v := range tt.want {
				want[k] = v
			}
			if !reflect.DeepEqual(srv.query[0], want) {
				t.Errorf("query = %v, want %v", srv.query[0], want)
			}
			if srv.agents[0] != testAgent {
				t.Errorf("User-Agent = %q, want %q", srv.agents[0], testAgent)
			}
		})
	}
}

func TestStationsMapping(t *testing.T) {
	const body = `[
	{"stationuuid":"a","name":"  SomaFM Groove Salad  ","url":"https://somafm.com/g.pls","url_resolved":"https://ice6.somafm.com/g","tags":"ambient, chillout,,  ,lounge","countrycode":"US","codec":"MP3","bitrate":128},
	{"stationuuid":"b","name":"Fallback","url":"http://fallback.example/s","url_resolved":"","tags":"","countrycode":"DE","codec":"AAC","bitrate":0},
	{"stationuuid":"c","name":"No URL","url":"","url_resolved":""},
	{"stationuuid":"d","name":"Duplicate","url_resolved":"https://ice6.somafm.com/g","bitrate":64},
	{"stationuuid":"e","name":"Dup via fallback","url":"http://fallback.example/s"},
	{"stationuuid":"f","name":"Other","url_resolved":"https://other.example/s"}
	]`
	srv := newRecorder(t, 200, body)
	c := NewWithServers(testAgent, []string{srv.URL})
	got, err := c.Stations(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	want := []Station{
		{UUID: "a", Name: "SomaFM Groove Salad", URL: "https://ice6.somafm.com/g", Codec: "MP3", Bitrate: 128, Country: "US", Tags: []string{"ambient", "chillout", "lounge"}},
		{UUID: "b", Name: "Fallback", URL: "http://fallback.example/s", Codec: "AAC", Country: "DE"},
		{UUID: "f", Name: "Other", URL: "https://other.example/s"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stations = %+v\nwant %+v", got, want)
	}
}

func TestFacets(t *testing.T) {
	tests := []struct {
		name  string
		kind  Kind
		path  string
		limit string
		body  string
		want  []Facet
	}{
		{
			name:  "genre",
			kind:  Genre,
			path:  "/json/tags",
			limit: "100",
			body:  `[{"name":"pop","stationcount":5119},{"name":"","stationcount":9},{"name":"empty","stationcount":0},{"name":"rock","stationcount":4000}]`,
			want: []Facet{
				{Kind: Genre, Name: "pop", Code: "pop", Count: 5119},
				{Kind: Genre, Name: "rock", Code: "rock", Count: 4000},
			},
		},
		{
			name: "country",
			kind: Country,
			path: "/json/countries",
			body: `[{"name":"Germany","iso_3166_1":"DE","stationcount":5597},{"name":"Nowhere","iso_3166_1":"","stationcount":4},{"name":"","iso_3166_1":"XX","stationcount":4},{"name":"Gone","iso_3166_1":"GG","stationcount":0},{"name":"France","iso_3166_1":"FR","stationcount":3000}]`,
			want: []Facet{
				{Kind: Country, Name: "Germany", Code: "DE", Count: 5597},
				{Kind: Country, Name: "France", Code: "FR", Count: 3000},
			},
		},
		{
			name:  "language",
			kind:  Language,
			path:  "/json/languages",
			limit: "100",
			body:  `[{"name":"english","iso_639":"en","stationcount":11283},{"name":"","iso_639":"xx","stationcount":2},{"name":"german","iso_639":"de","stationcount":0}]`,
			want:  []Facet{{Kind: Language, Name: "english", Code: "english", Count: 11283}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newRecorder(t, 200, tt.body)
			c := NewWithServers(testAgent, []string{srv.URL})
			got, err := c.Facets(context.Background(), tt.kind)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("facets = %+v, want %+v", got, tt.want)
			}
			if srv.paths[0] != tt.path {
				t.Errorf("path = %q, want %q", srv.paths[0], tt.path)
			}
			q := srv.query[0]
			if q.Get("order") != "stationcount" || q.Get("reverse") != "true" || q.Get("hidebroken") != "true" {
				t.Errorf("query = %v", q)
			}
			if q.Get("limit") != tt.limit {
				t.Errorf("limit = %q, want %q", q.Get("limit"), tt.limit)
			}
			if srv.agents[0] != testAgent {
				t.Errorf("User-Agent = %q", srv.agents[0])
			}
		})
	}
}

func TestFacetsUnknownKind(t *testing.T) {
	c := NewWithServers(testAgent, []string{"http://127.0.0.1:1"})
	if _, err := c.Facets(context.Background(), Kind(99)); err == nil {
		t.Error("want an error for an unknown kind")
	}
}

func TestClick(t *testing.T) {
	tests := []struct {
		name     string
		uuid     string
		wantPath string // empty means no request
	}{
		{"plain", "960cf833-0601", "/json/url/960cf833-0601"},
		{"escaped", "a/b c", "/json/url/a%2Fb%20c"},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newRecorder(t, 200, `{"ok":true,"url":"x"}`)
			c := NewWithServers(testAgent, []string{srv.URL})
			if err := c.Click(context.Background(), tt.uuid); err != nil {
				t.Fatal(err)
			}
			if tt.wantPath == "" {
				if srv.hits() != 0 {
					t.Errorf("got %d requests, want none", srv.hits())
				}
				return
			}
			if srv.hits() != 1 || srv.paths[0] != tt.wantPath {
				t.Errorf("paths = %v, want [%s]", srv.paths, tt.wantPath)
			}
		})
	}
}

func TestClickIgnoresBody(t *testing.T) {
	srv := newRecorder(t, 200, `not json at all`)
	c := NewWithServers(testAgent, []string{srv.URL})
	if err := c.Click(context.Background(), "u"); err != nil {
		t.Errorf("Click = %v, want nil", err)
	}
}

func TestFailover(t *testing.T) {
	good := newRecorder(t, 200, `[{"stationuuid":"a","url":"https://x.example/s"}]`)
	bad := newRecorder(t, 500, `oops`)
	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()

	tests := []struct {
		name    string
		servers []string
		failing func() int // hits on the failing server
	}{
		{"server error", []string{bad.URL, good.URL}, bad.hits},
		{"closed server", []string{closedURL, good.URL}, func() int { return 0 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := good.hits()
			beforeBad := tt.failing()
			c := NewWithServers(testAgent, tt.servers)
			for range 2 {
				got, err := c.Stations(context.Background(), Query{})
				if err != nil {
					t.Fatal(err)
				}
				if len(got) != 1 {
					t.Fatalf("got %d stations", len(got))
				}
			}
			if n := good.hits() - before; n != 2 {
				t.Errorf("good server got %d requests, want 2", n)
			}
			// The failing server is asked once, and not again after the
			// good server answered.
			if n := tt.failing() - beforeBad; n > 1 {
				t.Errorf("failing server got %d requests, want at most 1", n)
			}
		})
	}
}

func TestAllServersFail(t *testing.T) {
	a := newRecorder(t, 502, ``)
	b := newRecorder(t, 503, ``)
	c := NewWithServers(testAgent, []string{a.URL, b.URL})
	_, err := c.Stations(context.Background(), Query{})
	if err == nil {
		t.Fatal("want an error")
	}
	if want := "the station directory did not answer"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if errors.Unwrap(err) == nil {
		t.Error("the cause is not wrapped")
	}
	if a.hits() != 1 || b.hits() != 1 {
		t.Errorf("hits = %d, %d, want 1 each", a.hits(), b.hits())
	}
}

func TestNoFailoverOnClientError(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"4xx", 404, `{}`},
		{"bad request", 400, `[]`},
		{"bad json", 200, `<html>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := newRecorder(t, tt.status, tt.body)
			second := newRecorder(t, 200, `[]`)
			c := NewWithServers(testAgent, []string{first.URL, second.URL})
			if _, err := c.Stations(context.Background(), Query{}); err == nil {
				t.Fatal("want an error")
			}
			if second.hits() != 0 {
				t.Errorf("second server got %d requests, want none", second.hits())
			}
		})
	}
}

func TestContextCancelled(t *testing.T) {
	srv := newRecorder(t, 200, `[]`)
	c := NewWithServers(testAgent, []string{srv.URL})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Stations(ctx, Query{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestNewUsesSRV(t *testing.T) {
	var calls atomic.Int32
	c := New(testAgent)
	c.lookupSRV = func(_ context.Context, service, proto, name string) (string, []*net.SRV, error) {
		calls.Add(1)
		if service != "api" || proto != "tcp" || name != "radio-browser.info" {
			t.Errorf("lookup %q %q %q", service, proto, name)
		}
		return "", []*net.SRV{{Target: "de1.api.radio-browser.info."}, {Target: "nl1.api.radio-browser.info."}}, nil
	}
	servers, _, err := c.order(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range servers {
		got[s] = true
	}
	want := map[string]bool{"https://de1.api.radio-browser.info": true, "https://nl1.api.radio-browser.info": true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("servers = %v", servers)
	}
	if _, _, err := c.order(context.Background()); err != nil || calls.Load() != 1 {
		t.Errorf("lookups = %d, err = %v, want 1 lookup", calls.Load(), err)
	}
}

func TestNewFallback(t *testing.T) {
	tests := []struct {
		name string
		look func(context.Context, string, string, string) (string, []*net.SRV, error)
	}{
		{"error", func(context.Context, string, string, string) (string, []*net.SRV, error) {
			return "", nil, errors.New("no dns")
		}},
		{"empty", func(context.Context, string, string, string) (string, []*net.SRV, error) {
			return "", nil, nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New(testAgent)
			c.lookupSRV = tt.look
			servers, _, err := c.order(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(servers, []string{"https://all.api.radio-browser.info"}) {
				t.Errorf("servers = %v", servers)
			}
		})
	}
}

func TestNewLookupCancelled(t *testing.T) {
	c := New(testAgent)
	c.lookupSRV = func(ctx context.Context, _, _, _ string) (string, []*net.SRV, error) {
		return "", nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Facets(ctx, Genre); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	// A cancelled lookup must not fix the fallback for later requests.
	if c.resolved {
		t.Error("a cancelled lookup was kept")
	}
}

func TestConcurrentFirstRequest(t *testing.T) {
	srv := newRecorder(t, 200, `[]`)
	var calls atomic.Int32
	c := New(testAgent)
	c.lookupSRV = func(context.Context, string, string, string) (string, []*net.SRV, error) {
		calls.Add(1)
		return "", []*net.SRV{{Target: srv.Listener.Addr().String()}}, nil
	}
	// The target holds no scheme, so the requests fail, but the lookup must
	// still run only once.
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { _, _ = c.Stations(context.Background(), Query{}) })
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Errorf("lookups = %d, want 1", calls.Load())
	}
}

func TestStationsDropUnsafeURLs(t *testing.T) {
	const body = `[
	{"stationuuid":"a","url_resolved":"file:///etc/passwd","url":"https://ok.example/a"},
	{"stationuuid":"b","url_resolved":"javascript:alert(1)","url":""},
	{"stationuuid":"c","url_resolved":"ftp://x.example/s"},
	{"stationuuid":"d","url_resolved":"http:///nohost"},
	{"stationuuid":"e","url_resolved":"HTTPS://Upper.example/s"},
	{"stationuuid":"f","url_resolved":"rtmp://x.example/s","url":"concat:a|b"},
	{"stationuuid":"g","url_resolved":"://bad","url":"http://good.example/g"},
	{"stationuuid":"h","url_resolved":"/relative/path"}
	]`
	srv := newRecorder(t, 200, body)
	c := NewWithServers(testAgent, []string{srv.URL})
	got, err := c.Stations(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	urls := map[string]string{}
	for _, s := range got {
		urls[s.UUID] = s.URL
	}
	want := map[string]string{
		"a": "https://ok.example/a",
		"e": "HTTPS://Upper.example/s",
		"g": "http://good.example/g",
	}
	if !reflect.DeepEqual(urls, want) {
		t.Errorf("urls = %v, want %v", urls, want)
	}
}

// hangServer answers like a hung server: it sends nothing, or only the
// headers, until the test ends or the client gives up.
func hangServer(t *testing.T, flushHeaders bool) *recorder {
	t.Helper()
	r := &recorder{}
	release := make(chan struct{})
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.paths = append(r.paths, req.URL.Path)
		r.mu.Unlock()
		if flushHeaders {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
		}
		select {
		case <-release:
		case <-req.Context().Done():
		}
	}))
	// Cleanups run last in first out, so the handlers are freed before Close.
	t.Cleanup(r.Close)
	t.Cleanup(func() { close(release) })
	return r
}

func TestFailoverOnHungServer(t *testing.T) {
	tests := []struct {
		name  string
		flush bool // hang after the headers, while the body is read
	}{
		{"no answer", false},
		{"stalled body", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hung := hangServer(t, tt.flush)
			good := newRecorder(t, 200, `[{"stationuuid":"a","url":"https://x.example/s"}]`)
			c := NewWithServers(testAgent, []string{hung.URL, good.URL})
			c.attemptTimeout = 50 * time.Millisecond
			got, err := c.Stations(context.Background(), Query{})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Errorf("got %d stations, want 1", len(got))
			}
			if good.hits() != 1 {
				t.Errorf("good server got %d requests, want 1", good.hits())
			}
		})
	}
}

func TestCallerDeadlineStopsFailover(t *testing.T) {
	hung := hangServer(t, false)
	good := newRecorder(t, 200, `[]`)
	c := NewWithServers(testAgent, []string{hung.URL, good.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Stations(ctx, Query{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
	if good.hits() != 0 {
		t.Errorf("second server got %d requests, want none", good.hits())
	}
}

func TestWaiterStopsWhenContextEnds(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	c := New(testAgent)
	c.lookupSRV = func(context.Context, string, string, string) (string, []*net.SRV, error) {
		calls.Add(1)
		close(started)
		<-release
		return "", []*net.SRV{{Target: "de1.api.radio-browser.info."}}, nil
	}
	first := make(chan error, 1)
	go func() {
		_, _, err := c.order(context.Background())
		first <- err
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	second := make(chan error, 1)
	go func() {
		_, _, err := c.order(ctx)
		second <- err
	}()
	cancel()
	if err := <-second; !errors.Is(err, context.Canceled) {
		t.Errorf("waiter error = %v, want context.Canceled", err)
	}

	close(release)
	if err := <-first; err != nil {
		t.Errorf("first caller error = %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("lookups = %d, want 1", calls.Load())
	}
}

func TestWaiterRetriesAfterCancelledLookup(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	c := New(testAgent)
	c.lookupSRV = func(ctx context.Context, _, _, _ string) (string, []*net.SRV, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return "", nil, ctx.Err()
		}
		return "", []*net.SRV{{Target: "de1.api.radio-browser.info."}}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, _, err := c.order(ctx)
		first <- err
	}()
	<-started
	second := make(chan error, 1)
	go func() {
		_, _, err := c.order(context.Background())
		second <- err
	}()
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Errorf("first error = %v, want context.Canceled", err)
	}
	if err := <-second; err != nil {
		t.Errorf("second error = %v, want nil", err)
	}
}

func TestClickReusesConnection(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"url":"x"}`))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)
	c := NewWithServers(testAgent, []string{srv.URL})
	for range 3 {
		if err := c.Click(context.Background(), "u"); err != nil {
			t.Fatal(err)
		}
	}
	if n := conns.Load(); n != 1 {
		t.Errorf("connections = %d, want 1", n)
	}
}

func TestEndlessBodyIsBounded(t *testing.T) {
	chunk := []byte(strings.Repeat(" ", 64<<10))
	var sent atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("["))
		for {
			n, err := w.Write(chunk)
			sent.Add(int64(n))
			if err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	c := NewWithServers(testAgent, []string{srv.URL})
	if _, err := c.Stations(context.Background(), Query{}); err == nil {
		t.Fatal("want an error for an endless body")
	}
	if n := sent.Load(); n < maxBody {
		t.Errorf("server sent only %d bytes, the limit was not reached", n)
	}
}
