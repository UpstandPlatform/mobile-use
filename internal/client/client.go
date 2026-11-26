package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mobile-use/internal/wire"
)

type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

func New(endpoint, token string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("set a valid mobile-use origin")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return nil, fmt.Errorf("remote origins require HTTPS")
		}
	}
	return &Client{endpoint: strings.TrimRight(endpoint, "/"), token: token, http: &http.Client{
		Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) Request(ctx context.Context, method, path string, input, output any) error {
	if !strings.HasPrefix(path, "/v1/") || strings.HasPrefix(path, "//") {
		return fmt.Errorf("invalid API path")
	}
	var body io.Reader
	if input != nil {
		body = strings.NewReader(wire.Marshal(input))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	rd := io.LimitReader(response.Body, 13<<20)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload struct {
			Error *wire.Fault `json:"error"`
		}
		if err := json.NewDecoder(rd).Decode(&payload); err == nil && payload.Error != nil {
			payload.Error.Status = response.StatusCode
			return payload.Error
		}
		return fmt.Errorf("mobile-use returned HTTP %d", response.StatusCode)
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	if out, ok := output.(*json.RawMessage); ok {
		data, err := io.ReadAll(rd)
		if err != nil {
			return err
		}
		if !json.Valid(data) {
			return fmt.Errorf("invalid API response")
		}
		*out = data
		return nil
	}
	return json.NewDecoder(rd).Decode(output)
}
