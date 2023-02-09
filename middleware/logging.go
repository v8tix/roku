package middleware

import (
	"log"
	"net/http"
)

type LoggingTransport struct {
	log *log.Logger
}

func (l LoggingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	l.log.Printf(
		"Sending a %s request to %s over %s\n",
		r.Method, r.URL, r.Proto,
	)
	resp, err := http.DefaultTransport.RoundTrip(r)
	l.log.Printf("Got back a response over %s\n", resp.Proto)
	return resp, err
}
