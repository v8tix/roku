package middleware

import "net/http"

type CustomHeaders struct {
	headers map[string]string
}

func NewCustomHeaders(headers map[string]string) CustomHeaders {
	cHeaders := CustomHeaders{
		headers: headers,
	}
	return cHeaders
}

func (c CustomHeaders) RoundTrip(r *http.Request) (*http.Response, error) {
	reqCopy := r.Clone(r.Context())
	for k, v := range c.headers {
		reqCopy.Header.Add(k, v)
	}
	return http.DefaultTransport.RoundTrip(reqCopy)
}
