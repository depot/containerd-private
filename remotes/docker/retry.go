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
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/containerd/containerd/pkg/randutil"
)

const maxRetryDelay = 15 * time.Second

// retryDelay returns a positive delay only for rate-limited requests.
// Retry-After takes precedence over jittered exponential backoff.
func retryDelay(resp *http.Response, attempt int) time.Duration {
	if resp.StatusCode != http.StatusTooManyRequests {
		return 0
	}

	delay, ok := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
	if !ok || delay <= 0 {
		if attempt < 1 {
			attempt = 1
		} else if attempt > 5 {
			attempt = 5
		}
		delay = time.Second << (attempt - 1)
	}
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}

	delay = jitter(delay)
	if delay > maxRetryDelay {
		return maxRetryDelay
	}
	return delay
}

func jitter(delay time.Duration) time.Duration {
	if delay < 2 {
		return delay
	}
	return delay*3/4 + time.Duration(randutil.Int63n(int64(delay)/2))
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		if seconds > uint64(math.MaxInt64/int64(time.Second)) {
			return math.MaxInt64, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	if date, err := http.ParseTime(value); err == nil {
		return date.Sub(now), true
	}
	return 0, false
}
