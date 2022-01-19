package phttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v4"
	"golang.org/x/time/rate"
)

// DefaultBackOff is an opinionated backoff.ExponentialBackOff which implements the backoff.BackOff interface.
var DefaultBackOff = backoff.ExponentialBackOff{
	InitialInterval:     500 * time.Millisecond,
	RandomizationFactor: 0.5,
	Multiplier:          1.5,
	MaxInterval:         5 * time.Second,
	MaxElapsedTime:      30 * time.Second,
	Stop:                backoff.Stop,
	Clock:               backoff.SystemClock,
}

// DefaultRateLimiter is an opinionated rate.Limiter which implements the Waiter interface.
var DefaultRateLimiter = rate.NewLimiter(rate.Limit(1), 1)

// Client provides all required functionality for
// 1. performing http requests with a Requester
// 2. while not exceeding a defined rate limit with a Waiter
// 3. temporary errors can be retried with a backoff.BackOff
type Client struct {
	Requester Requester
	Waiter    Waiter
	Backoff   backoff.BackOff
}

// Requester is this libraries interface for a http.Client.
// This is due to https://github.com/golang/go/issues/16047
type Requester interface {
	Do(req *http.Request) (*http.Response, error)
}

// Waiter should block when Wait is called, until events are allowed to happen.
// It should return an error if the Context is canceled, or the expected wait time exceeds the Context's Deadline.
type Waiter interface {
	Wait(ctx context.Context) error
}

// Option is a function to alter the behaviour of a Client.
type Option func(c *Client)

// WithHttpClient configures the Client to use the given Requester.
// http.DefaultClient implements the Requester interface.
func WithHttpClient(requester Requester) Option {
	return func(c *Client) {
		c.Requester = requester
	}
}

// WithRateLimiter configures the Client to use the given Waiter.
func WithRateLimiter(waiter Waiter) Option {
	return func(c *Client) {
		c.Waiter = waiter
	}
}

// WithBackOff configures the Client to use the given backoff.BackOff.
func WithBackOff(bo backoff.BackOff) Option {
	return func(c *Client) {
		c.Backoff = bo
	}
}

// NewWithDefaults returns a client with the two default options for rate
// limiting and backoff DefaultRateLimiter and DefaultBackOff
func NewWithDefaults() *Client {
	return New(WithRateLimiter(DefaultRateLimiter), WithBackOff(&DefaultBackOff))
}

// New creates a Client and accepts Options to configure it.
func New(opts ...Option) *Client {
	client := &Client{
		Requester: http.DefaultClient,
		Waiter:    nil,
		Backoff:   nil,
	}

	for _, opt := range opts {
		opt(client)
	}

	return client
}

// Do is the interface for http.Client.Do.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if c.Backoff == nil {
		return c.do(req)
	}

	var resp *http.Response

	operation := func() error {
		var err error

		resp, err = c.do(req)
		if err != nil {
			return err
		}

		return nil
	}

	err := backoff.Retry(operation, c.Backoff)
	if err != nil {
		return nil, fmt.Errorf("exhausted all retries: %w", err)
	}

	return resp, nil
}

// HTTPError exposes the http.Response while also giving some convenience for the http status code & response body.
type HTTPError struct {
	Code     int
	Body     string
	Response *http.Response
}

// Error will print the http status code and optionally the http response body.
func (e HTTPError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("failed HTTP call: %d: %s", e.Code, e.Body)
	}

	return fmt.Sprintf("failed HTTP call: %d", e.Code)
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	if c.Waiter != nil {
		err := c.Waiter.Wait(req.Context())
		if err != nil {
			return nil, err
		}
	}

	resp, err := c.Requester.Do(req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode > 399 && resp.StatusCode < 500 {
		bodyBytes, err := readHTTPBody(resp.Body)
		if err != nil {
			return nil, backoff.Permanent(HTTPError{
				Code:     resp.StatusCode,
				Response: resp,
			})
		}

		return nil, backoff.Permanent(HTTPError{
			Code:     resp.StatusCode,
			Body:     string(bodyBytes),
			Response: resp,
		})
	}

	//TODO: Add Retry-After parsing if it's existing
	// Implementation might be assuming worst case
	// that the retry is the minimal of c.EBackoff
	// so we sleep for Retry-After - c.EBackoff.InitialInterval
	// that should put us either right on or slightly above the
	// desired value of the system.
	// Max wait time would be Retry-After + c.EBackoff.MaxInterval
	return resp, nil
}

func readHTTPBody(bodyReader io.ReadCloser) ([]byte, error) {
	defer bodyReader.Close()
	return io.ReadAll(bodyReader)
}
