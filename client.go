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
	"strings"
	"time"
)

const (
	Get           = HttpMethod("GET")
	Post          = HttpMethod("POST")
	Put           = HttpMethod("PUT")
	Patch         = HttpMethod("PATCH")
	Delete        = HttpMethod("DELETE")
	RetryInterval = 15 * time.Second
	ConnTimeOut   = 15 * time.Second
	DeadLine      = 10 * time.Second
)

var (
	ErrTimeOut                  = errors.New("time out")
	ErrMarshallValue            = errors.New("couldn't marshalling the value")
	ErrBadRequest               = errors.New("bad request")
	ErrNonPointerOrWrongCasting = errors.New("RxGo item value is not a pointer or you are using the wrong casting type")
	ErrEmptyItem                = errors.New("RxGo item has no value and no error")
	ErrNilValue                 = errors.New("nil value provided")
	ErrTimerNotSet              = errors.New("timer must be set")
)

type ReqI interface {
	Req()
}

type ResI interface {
	Res()
}

type NoReq string

func (nrq NoReq) Req() {}

type NoRes struct{}

func (nrp NoRes) Res() {}

type Envelope[T ResI] struct {
	Body *T
	*http.Response
}

func newResponse[T ResI](pBody *T, res *http.Response) *Envelope[T] {
	r := Envelope[T]{
		Body:     pBody,
		Response: res,
	}
	return &r
}

type ErrInvalidHttpStatus struct {
	Res *http.Response
}

type ErrProps struct {
	StatusCode int    `json:"status_code,omitempty"`
	Status     string `json:"status,omitempty"`
	ErrMessage string `json:"error_message,omitempty"`
}

func NewErrParams(statusCode int, status string, errMessage string) *ErrProps {
	errParams := ErrProps{StatusCode: statusCode, Status: status, ErrMessage: errMessage}
	return &errParams
}

func (e ErrInvalidHttpStatus) Error() string {
	msg, err := io.ReadAll(e.Res.Body)
	if err != nil {
		return ""
	}
	errParams := NewErrParams(e.Res.StatusCode, e.Res.Status, string(msg))
	errParamsMarshalled, err := json.Marshal(errParams)
	if err != nil {
		return ""
	}
	return string(errParamsMarshalled)
}

func (e ErrInvalidHttpStatus) UnmarshalError() (ErrProps, error) {
	var errProps ErrProps

	reader := bytes.NewReader([]byte(e.Error()))
	err := ReadJSON(reader, &errProps)
	if err != nil {
		return ErrProps{}, err
	}

	return errProps, nil
}

func GetErrorProperties(err error) ErrProps {
	if !errors.As(err, &ErrInvalidHttpStatus{}) {
		return ErrProps{}
	}

	var errInv ErrInvalidHttpStatus
	errors.As(err, &errInv)
	errorProps, err := errInv.UnmarshalError()
	if err != nil {
		return ErrProps{}
	}

	return errorProps
}

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

func FetchRx[T ReqI, U ResI](
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

func Fetch[T ReqI, U ResI](
	ctx context.Context,
	httpClient *http.Client,
	method HttpMethod,
	endpoint string,
	req *T,
	headers map[string]string,
	deadline time.Duration,
	statusCodeValidator func(res *http.Response) bool,
) (*Envelope[U], error) {
	var body U
	var httpResponse *http.Response
	var timer *time.Timer
	var data []byte
	var err error

	reader, err := toBytesReader[T](req)
	switch err {
	case nil:
		break
	default:
		if !errors.Is(err, ErrNilValue) {
			return nil, err
		}
	}

	switch reader {
	case nil:
		httpResponse, timer, err = httpCall(ctx, httpClient, endpoint, headers, nil, deadline, method, statusCodeValidator)
		if err != nil {
			return nil, err
		}
	default:
		httpResponse, timer, err = httpCall(ctx, httpClient, endpoint, headers, reader, deadline, method, statusCodeValidator)
		if err != nil {
			return nil, err
		}
	}

	if httpResponse.StatusCode != http.StatusNoContent {
		data, err = readBody(timer, httpResponse.Body)
		if err != nil {
			return nil, err
		}

		err = ReadJSON(bytes.NewReader(data), &body)
		if err != nil {
			return nil, err
		}

		return newResponse[U](&body, httpResponse), nil
	}

	return newResponse[U](nil, httpResponse), nil
}

func httpCall(
	ctx context.Context,
	client *http.Client,
	url string,
	headers map[string]string,
	body io.Reader,
	deadline time.Duration,
	method HttpMethod,
	statusCodeValidator func(res *http.Response) bool,
) (*http.Response, *time.Timer, error) {
	return do(ctx, client, url, method, headers, body, deadline, statusCodeValidator)
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

func DefaultInvalidStatusCodeValidator(res *http.Response) bool {
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

func To[T any](item rxgo.Item) (itemPointer *T, err error) {
	switch item.V.(type) {
	case *T:
		return item.V.(*T), nil
	case nil:
		if item.E != nil {
			return nil, item.E
		} else {
			return nil, ErrEmptyItem
		}
	case error:
		return nil, item.V.(error)
	default:
		return nil, ErrNonPointerOrWrongCasting
	}
}

func readBody(timer *time.Timer, rc io.ReadCloser) (data []byte, err error) {
	defer func(rc io.ReadCloser) {
		err = rc.Close()
	}(rc)

	if timer == nil {
		return nil, ErrTimerNotSet
	}

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

func ReadJSON(body io.Reader, dst any) error {
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()

	err := dec.Decode(dst)
	if err != nil {
		var syntaxError *json.SyntaxError
		var unmarshalTypeError *json.UnmarshalTypeError
		var invalidUnmarshalError *json.InvalidUnmarshalError
		var maxBytesError *http.MaxBytesError

		switch {
		case errors.As(err, &syntaxError):
			return fmt.Errorf("body contains badly-formed JSON (at character %d)", syntaxError.Offset)

		case errors.Is(err, io.ErrUnexpectedEOF):
			return errors.New("body contains badly-formed JSON")

		case errors.As(err, &unmarshalTypeError):
			if unmarshalTypeError.Field != "" {
				return fmt.Errorf("body contains incorrect JSON type for field %q", unmarshalTypeError.Field)
			}
			return fmt.Errorf("body contains incorrect JSON type (at character %d)", unmarshalTypeError.Offset)

		case errors.Is(err, io.EOF):
			return errors.New("body must not be empty")

		case strings.HasPrefix(err.Error(), "json: unknown field "):
			fieldName := strings.TrimPrefix(err.Error(), "json: unknown field ")
			return fmt.Errorf("body contains unknown key %s", fieldName)

		case errors.As(err, &maxBytesError):
			return fmt.Errorf("body must not be larger than %d bytes", maxBytesError.Limit)

		case errors.As(err, &invalidUnmarshalError):
			panic(err)

		default:
			return err
		}
	}

	err = dec.Decode(&struct{}{})
	if !errors.Is(err, io.EOF) {
		return errors.New("body must only contain a single JSON value")
	}

	return nil
}
