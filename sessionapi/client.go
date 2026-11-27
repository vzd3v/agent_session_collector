package sessionapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	Transcript = "transcript"
	Archive    = "archive"
)

// HTTPError reports a failed API request without retaining response content.
type HTTPError struct {
	Status int
}

func (e *HTTPError) Error() string { return fmt.Sprintf("session read API returned HTTP %d", e.Status) }

// Client is safe for concurrent requests.
type Client struct {
	http  http.Client
	base  *url.URL
	token string
}

// NewClient accepts HTTPS or loopback HTTP. A caller using a Unix socket can
// supply a transport that dials the socket and a loopback URL for HTTP framing.
func NewClient(endpoint, token string, client *http.Client) (*Client, error) {
	base, err := url.Parse(endpoint)
	if err != nil || base.User != nil || base.Hostname() == "" || base.RawQuery != "" || base.Fragment != "" ||
		(base.Scheme != "https" && (base.Scheme != "http" || !isLoopback(base.Hostname()))) {
		return nil, errors.New("session API URL requires HTTPS or loopback HTTP")
	}
	if token == "" || client == nil || strings.ContainsAny(token, " \t\r\n") {
		return nil, errors.New("session API token and HTTP client are required")
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{http: c, base: base, token: token}, nil
}

func isLoopback(name string) bool {
	return name == "localhost" || name == "127.0.0.1" || name == "::1"
}

func (c *Client) List(ctx context.Context, q Query) (Page, error) {
	if q.SourceIDs != nil && len(q.SourceIDs) == 0 {
		if q.Cursor != "" {
			return Page{}, &HTTPError{Status: http.StatusBadRequest}
		}
		return Page{Sessions: []Session{}}, nil
	}
	var result Page
	body, err := json.Marshal(q)
	if err != nil {
		return result, err
	}
	response, err := c.request(ctx, http.MethodPost, []string{"sessions", "query"}, nil, "", bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	err = decodeJSON(response, &result)
	return result, err
}

func (c *Client) Get(ctx context.Context, ref Ref) (Session, error) {
	if !validRef(ref) {
		return Session{}, errors.New("invalid session reference")
	}
	values := url.Values{}
	set(values, "sha256", ref.SHA256)
	var result Session
	err := c.getJSON(ctx, refPath(ref), values, &result)
	return result, err
}

func (c *Client) Read(ctx context.Context, q ReadQuery) (Events, error) {
	if !validRef(q.Ref) {
		return Events{}, errors.New("invalid session reference")
	}
	values := url.Values{}
	set(values, "sha256", q.SHA256)
	setPage(values, q.Cursor, q.Limit)
	var result Events
	err := c.getJSON(ctx, append(refPath(q.Ref), "events"), values, &result)
	return result, err
}

// Open returns the response for a download. The caller owns its body. A
// transcript response negotiates acceptEncoding and is left encoded.
func (c *Client) Open(ctx context.Context, ref Ref, format, acceptEncoding string) (*http.Response, error) {
	if !validRef(ref) {
		return nil, errors.New("invalid session reference")
	}
	if format != Transcript && format != Archive {
		return nil, errors.New("unknown session download format")
	}
	values := url.Values{}
	set(values, "sha256", ref.SHA256)
	encoding := "identity"
	if format == Transcript && acceptEncoding != "" {
		encoding = acceptEncoding
	}
	return c.get(ctx, append(refPath(ref), format), values, encoding)
}

func (c *Client) getJSON(ctx context.Context, segments []string, values url.Values, out any) error {
	res, err := c.get(ctx, segments, values, "")
	if err != nil {
		return err
	}
	return decodeJSON(res, out)
}

func decodeJSON(res *http.Response, out any) error {
	defer func() { _ = res.Body.Close() }()
	reader := &io.LimitedReader{R: res.Body, N: (16 << 20) + 1}
	dec := json.NewDecoder(reader)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("decode session API response: %w", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) || reader.N == 0 {
		return errors.New("invalid trailing session API response")
	}
	return nil
}

func (c *Client) get(ctx context.Context, segments []string, values url.Values, encoding string) (*http.Response, error) {
	return c.request(ctx, http.MethodGet, segments, values, encoding, nil)
}

func (c *Client) request(ctx context.Context, method string, segments []string, values url.Values, encoding string, body io.Reader) (*http.Response, error) {
	for i, part := range segments {
		segments[i] = url.PathEscape(part)
	}
	endpoint := c.base.JoinPath(append([]string{"api", "v1"}, segments...)...)
	endpoint.RawQuery = values.Encode()
	req, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if encoding != "" {
		req.Header.Set("Accept-Encoding", encoding)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		_ = res.Body.Close()
		return nil, &HTTPError{Status: res.StatusCode}
	}
	return res, nil
}

func refPath(ref Ref) []string { return []string{"sources", ref.SourceID, "sessions", ref.ArtifactID} }

func validRef(ref Ref) bool {
	return ValidSourceID(ref.SourceID) && ValidArtifactID(ref.ArtifactID) && (ref.SHA256 == "" || ValidSHA256(ref.SHA256))
}

func set(values url.Values, key, value string) {
	if value != "" {
		values.Set(key, value)
	}
}

func setPage(values url.Values, cursor string, limit int) {
	set(values, "cursor", cursor)
	if limit != 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
}
