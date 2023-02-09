package policy

import (
	"errors"
	"net/http"
)

var (
	ErrMoreThanOneRedirect = errors.New("stopped after 1 redirect")
)

func OneRedirect(_ *http.Request, via []*http.Request) error {
	if len(via) > 1 {
		return ErrMoreThanOneRedirect
	}
	return nil
}
