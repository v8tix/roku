package transport

import (
	"net/http"
	"time"
)

func IdleConnectionTimeout(timeout time.Duration) *http.Transport {
	transport := http.Transport{
		IdleConnTimeout: timeout,
	}
	return &transport
}
