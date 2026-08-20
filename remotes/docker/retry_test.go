/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package docker

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{name: "delta seconds", value: "2", want: 2 * time.Second, ok: true},
		{name: "zero delta seconds", value: "0", ok: true},
		{name: "HTTP date", value: "Thu, 20 Aug 2026 12:00:03 GMT", want: 3 * time.Second, ok: true},
		{name: "past HTTP date", value: "Thu, 20 Aug 2026 11:59:00 GMT", want: -time.Minute, ok: true},
		{name: "duration overflow saturates", value: "10000000000", want: math.MaxInt64, ok: true},
		{name: "empty", value: ""},
		{name: "negative", value: "-1"},
		{name: "fractional", value: "2.5"},
		{name: "invalid", value: "soon"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseRetryAfter(tc.value, now)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("parseRetryAfter(%q) = %v, %v; want %v, %v", tc.value, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestRetryDelay(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		retryAfter string
		attempt    int
		min        time.Duration
		max        time.Duration
	}{
		{name: "request timeout stays immediate", status: http.StatusRequestTimeout, attempt: 1},
		{name: "server error stays immediate", status: http.StatusInternalServerError, attempt: 1},
		{name: "Retry-After", status: http.StatusTooManyRequests, retryAfter: "2", attempt: 1, min: 1500 * time.Millisecond, max: 2500 * time.Millisecond},
		{name: "Retry-After capped", status: http.StatusTooManyRequests, retryAfter: "3600", attempt: 1, min: 11250 * time.Millisecond, max: maxRetryDelay},
		{name: "overflow capped", status: http.StatusTooManyRequests, retryAfter: "10000000000", attempt: 1, min: 11250 * time.Millisecond, max: maxRetryDelay},
		{name: "zero falls back", status: http.StatusTooManyRequests, retryAfter: "0", attempt: 1, min: 750 * time.Millisecond, max: 1250 * time.Millisecond},
		{name: "invalid falls back", status: http.StatusTooManyRequests, retryAfter: "soon", attempt: 2, min: 1500 * time.Millisecond, max: 2500 * time.Millisecond},
		{name: "low attempt clamps", status: http.StatusTooManyRequests, attempt: 0, min: 750 * time.Millisecond, max: 1250 * time.Millisecond},
		{name: "high attempt clamps", status: http.StatusTooManyRequests, attempt: 63, min: 11250 * time.Millisecond, max: maxRetryDelay},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{StatusCode: tc.status, Header: make(http.Header)}
			resp.Header.Set("Retry-After", tc.retryAfter)
			for i := 0; i < 20; i++ {
				got := retryDelay(resp, tc.attempt)
				if got < tc.min || got > tc.max {
					t.Fatalf("retryDelay(%d, %q, %d) = %v; want [%v, %v]", tc.status, tc.retryAfter, tc.attempt, got, tc.min, tc.max)
				}
			}
		})
	}
}

func TestRetryDelayPastHTTPDateFallsBack(t *testing.T) {
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header)}
	resp.Header.Set("Retry-After", time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat))
	if got := retryDelay(resp, 1); got < 750*time.Millisecond || got > 1250*time.Millisecond {
		t.Fatalf("retryDelay with past HTTP date = %v; want [750ms, 1.25s]", got)
	}
}

func TestDoWithRetriesCanceledDuringBackoff(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	req := &request{
		method: http.MethodGet,
		host: RegistryHost{
			Client: server.Client(),
			Host:   u.Host,
			Scheme: u.Scheme,
		},
		retryNotify: func(context.Context, string, int, int, time.Duration) {
			cancel()
		},
	}

	resp, err := req.doWithRetries(ctx, nil)
	if resp != nil {
		resp.Body.Close()
		t.Fatal("doWithRetries() returned a response; want nil")
	}
	if err != context.Canceled {
		t.Fatalf("doWithRetries() error = %v; want %v", err, context.Canceled)
	}
}
