package pbhttp

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v4"
	"golang.org/x/time/rate"
)

type Client struct {
	HTTPClient  *http.Client
	RateLimiter *rate.Limiter
	EBackoff    *backoff.ExponentialBackoff
}

type ClientConfig struct {
	MaxRPS   int
	Timeout  time.Duration
	EBackoff *backoff.ExponentialBackoff
}

func New(cfg ClientConfig) *Client {
	if cfg.EBackoff != nil {
		return &Client{
			HTTPClient: http.Client{
				Timeout: cfg.Timeout,
			},
			RateLimiter: rate.NewLimiter(rate.Limit(cfg.MaxRPS), MaxRPS),
			EBackoff:    cfg.EBackoff,
		}
	}
	return &Client{
		HTTPClient: http.Client{
			Timeout: cfg.Timeout,
		},
		RateLimiter: rate.NewLimiter(rate.Limit(cfg.MaxRPS), MaxRPS),
		EBackoff: &backoff.ExponentialBackOff{
			InitialInterval:     500 * time.Millisecond,
			RandomizationFactor: 0.5,
			Multiplier:          1.5,
			MaxInterval:         5 * time.Second,
			MaxElapsedTime:      30 * time.Second,
			Stop:                backoff.Stop,
			Clock:               backoff.SystemClock,
		},
	}
}

func (c *Client) Do(req *http.Request) (*http.Response, error) {
	resp, err := backoff.Retry(do, c.EBackoff)
	if err != nil {
		return fmt.Errorf("exhausted all retries: %w", err)
	}
}

type HTTPError struct {
	Code     int
	Body     string
	Response *http.Response
}

func (he *HTTPError) Error() string {
	if he.Body != "" {
		return fmt.Sprintf("failed HTTP call: %d: %s", he.Code, he.Body)
	}
	return fmt.Sprintf("failed HTTP call: %d", he.Code)
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	err := c.RateLimiter.Wait(req.Context())
	if err != nil {
		return nil, err
	}

	resp, err := c.Client.Do(req)
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
		return nil, backoff.Permament(HTTPError{
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

	bodyBytes, err := io.ReadAll(bodyReader)
	if err != nil {
		return nil, err
	}

	return bodyBytes, nil
}
