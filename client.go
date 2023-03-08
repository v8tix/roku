package roku

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cenkalti/backoff/v4"
	"github.com/reactivex/rxgo/v2"
	"io"
	"net/http"
	"time"
)

const (
	Get           = HttpMethod("GET")
	Post          = HttpMethod("POST")
	Put           = HttpMethod("PUT")
	Patch         = HttpMethod("PATCH")
	Delete        = HttpMethod("DELETE")
	Duration      = 15 * time.Second
	ConnTimeOut   = 15 * time.Second
	DeadLine      = 10 * time.Second
	MaxRetries    = 1
	RetryInterval = 10 * time.Millisecond
)

var (
	ErrTimeOut                  = errors.New("time out")
	ErrMarshallValue            = errors.New("couldn't marshalling the value")
	ErrCannotCast               = errors.New("couldn't cast nil interface")
	ErrBadRequest               = errors.New("bad request")
	ErrNonPointerOrWrongCasting = errors.New("RxGo item value is not a pointer or you are using the wrong casting type")
	ErrEmptyItem                = errors.New("RxGo item has no value and no error")
	ErrNilValue                 = errors.New("nil value provided")
	ErrOperationNotAllowed      = errors.New("operation not allowed")
)

type Req interface {
	Req()
}

type Res interface {
	Res()
}

type ErrInvalidHttpStatus struct {
	Res *http.Response
}

func (e ErrInvalidHttpStatus) Error() string {
	return fmt.Sprintf("response:{status_code: %d, status: %s}", e.Res.StatusCode, e.Res.Status)
}

type BiRes[T, R Res] struct {
	Res1 *T
	Res2 *R
}

func (pr BiRes[T, R]) Res() {}

type HttpMethod string

func NewHTTPClient(
	timeout time.Duration,
	redirectPolicy func(req *http.Request, via []*http.Request) error,
	roundTripper http.RoundTripper,
) *http.Client {
	httpClient := http.Client{
		Timeout:       timeout,
		Transport:     roundTripper,
		CheckRedirect: redirectPolicy,
	}
	return &httpClient
}

func FetchRx[T Req, U Res](
	ctx context.Context,
	httpClient *http.Client,
	method HttpMethod,
	endpoint string,
	req *T,
	headers map[string]string,
	deadline time.Duration,
	retryInterval time.Duration,
	retries uint64,
	statusCodeValidator func(res *http.Response) bool,
) rxgo.Observable {
	backOffCfg := backoff.NewExponentialBackOff()
	backOffCfg.InitialInterval = retryInterval

	return rxgo.Defer([]rxgo.Producer{
		func(_ context.Context, next chan<- rxgo.Item) {
			res, err := Fetch[T, U](
				ctx,
				httpClient,
				method,
				endpoint,
				req,
				headers,
				deadline,
				statusCodeValidator,
			)
			if err != nil {
				next <- rxgo.Error(err)
			}
			next <- rxgo.Of(res)
		},
	}).BackOffRetry(backoff.WithMaxRetries(backOffCfg, retries))
}

func FetchBiParallel[T, U Res](requests ...rxgo.Observable) (*T, *U, error) {
	ch := rxgo.CombineLatest(
		func(i ...interface{}) interface{} {
			res, err := buildBiRes[T, U](i)
			if err != nil {
				return err
			}

			return res
		},
		requests,
	).Observe()

	casted, err := castRxGoItemTo[BiRes[T, U]](<-ch)
	if err != nil {
		return nil, nil, err
	}

	r1, r2, err := biCast[T, U](casted.Res1, casted.Res2)
	if err != nil {
		return nil, nil, err
	}

	return r1, r2, nil
}

func buildBiRes[T, R Res](items []interface{}) (*BiRes[T, R], error) {
	var res1 *T
	var res2 *R
	var err error

	for _, v := range items {
		switch v.(type) {
		case *T:
			res1 = v.(*T)
		case *R:
			res2 = v.(*R)
		case nil:
			return nil, ErrNilValue
		default:
			err = v.(error)
		}
	}

	if err != nil {
		return nil, err
	}

	pRes := BiRes[T, R]{
		Res1: res1,
		Res2: res2,
	}

	return &pRes, nil
}

func Fetch[T Req, U Res](
	ctx context.Context,
	httpClient *http.Client,
	method HttpMethod,
	endpoint string,
	req *T,
	headers map[string]string,
	deadline time.Duration,
	statusCodeValidator func(res *http.Response) bool,
) (*U, error) {
	var response U
	var res *http.Response
	var timer *time.Timer
	var reader *bytes.Reader
	var data []byte
	var err error

	if method == Post || method == Put || method == Patch {
		reader, err = toBytesReader[T](req)
		if err != nil {
			return nil, err
		}
	}

	switch method {
	case Get:
		res, timer, err = httpGet(ctx, httpClient, endpoint, headers, deadline, statusCodeValidator)
		if err != nil {
			return nil, err
		}
	case Post:
		res, timer, err = httpUpsert(ctx, httpClient, endpoint, headers, reader, deadline, Post, statusCodeValidator)
		if err != nil {
			return nil, err
		}
	case Put:
		res, timer, err = httpUpsert(ctx, httpClient, endpoint, headers, reader, deadline, Put, statusCodeValidator)
		if err != nil {
			return nil, err
		}
	case Patch:
		res, timer, err = httpUpsert(ctx, httpClient, endpoint, headers, reader, deadline, Patch, statusCodeValidator)
		if err != nil {
			return nil, err
		}
	case Delete:
		res, timer, err = httpDelete(ctx, httpClient, endpoint, headers, deadline, statusCodeValidator)
		if err != nil {
			return nil, err
		}
	default:
		return nil, ErrOperationNotAllowed
	}

	data, err = read(timer, res.Body)
	if err != nil {
		return nil, err
	}

	err = json.Unmarshal(data, &response)
	if err != nil {
		return nil, err
	}

	return &response, nil
}

func httpGet(
	ctx context.Context,
	client *http.Client,
	url string,
	headers map[string]string,
	deadline time.Duration,
	statusCodeValidator func(res *http.Response) bool,
) (*http.Response, *time.Timer, error) {
	return do(ctx, client, url, Get, headers, nil, deadline, statusCodeValidator)
}

func httpDelete(
	ctx context.Context,
	client *http.Client,
	url string,
	headers map[string]string,
	deadline time.Duration,
	statusCodeValidator func(res *http.Response) bool,
) (*http.Response, *time.Timer, error) {
	return do(ctx, client, url, Delete, headers, nil, deadline, statusCodeValidator)
}

func httpUpsert(
	ctx context.Context,
	client *http.Client,
	url string,
	headers map[string]string,
	body io.Reader,
	deadline time.Duration,
	method HttpMethod,
	statusCodeValidator func(res *http.Response) bool,
) (*http.Response, *time.Timer, error) {
	if method == Post || method == Put || method == Patch {
		return do(ctx, client, url, method, headers, body, deadline, statusCodeValidator)
	}
	return nil, nil, ErrOperationNotAllowed
}

func do(
	ctx context.Context,
	client *http.Client,
	url string,
	method HttpMethod,
	headers map[string]string,
	body io.Reader,
	deadline time.Duration,
	statusCodeValidator func(res *http.Response) bool,
) (*http.Response, *time.Timer, error) {
	var res *http.Response
	var err error

	toCtx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(deadline, func() {
		cancel()
	})

	req, err := http.NewRequestWithContext(toCtx, string(method), url, body)
	if err != nil {
		return nil, nil, err
	}

	if headers != nil {
		for k, v := range headers {
			req.Header.Add(k, v)
		}
	}

	res, err = client.Do(req)
	switch err {
	case nil:

		if statusCodeValidator(res) {
			return nil, timer, ErrInvalidHttpStatus{res}
		}
		return res, timer, nil

	default:

		if errors.Is(err, context.Canceled) {
			return nil, nil, fmt.Errorf("service at %q %w", req.URL, ErrTimeOut)
		}
		return nil, nil, err
	}
}

func defaultInvalidStatusCodeValidator(res *http.Response) bool {
	//validate all 4XX and 5XX error status codes
	return (res.StatusCode/100)/4 == 1 || (res.StatusCode/100)/5 == 1
}

func toBytesReader[T any](value *T) (*bytes.Reader, error) {
	switch value {
	case nil:
		return nil, ErrNilValue
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", err.Error(), ErrMarshallValue)
		}
		return bytes.NewReader(data), nil
	}
}

func monoCast[T Res](p1 interface{}) (*T, error) {
	switch p1.(type) {
	case *T:
		return p1.(*T), nil
	default:
		return nil, ErrCannotCast
	}
}

func biCast[T, U Res](p1, p2 interface{}) (*T, *U, error) {
	c1, err := monoCast[T](p1)
	if err != nil {
		return nil, nil, err
	}

	c2, err := monoCast[U](p2)
	if err != nil {
		return nil, nil, err
	}

	return c1, c2, nil
}

func unmarshalReq[T Req](req *http.Request) (*T, error) {
	var genReq T

	if req == nil {
		return nil, ErrNilValue
	}

	data, err := read(nil, req.Body)
	if err != nil {
		return nil, err
	}

	err = json.Unmarshal(data, &genReq)
	if err != nil {
		return nil, fmt.Errorf("%q: %w", err.Error(), ErrBadRequest)
	}

	return &genReq, nil
}

func castRxGoItemTo[T any](item rxgo.Item) (itemPointer *T, err error) {
	switch item.V.(type) {
	case *T:
		return item.V.(*T), nil
	case nil:
		if item.E != nil {
			return nil, fmt.Errorf("%q: %w", item.E.Error(), ErrNilValue)
		} else {
			return nil, ErrEmptyItem
		}
	case error:
		return nil, item.V.(error)
	default:
		return nil, ErrNonPointerOrWrongCasting
	}
}

func read(timer *time.Timer, rc io.ReadCloser) (data []byte, err error) {
	defer func(rc io.ReadCloser) {
		err = rc.Close()
	}(rc)

	if timer != nil {
		buf := new(bytes.Buffer)

		for {
			timer.Reset(10 * time.Millisecond)
			_, err = io.CopyN(buf, rc, 256)
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return nil, err
			}
		}

		return buf.Bytes(), nil
	}

	return io.ReadAll(rc)
}
