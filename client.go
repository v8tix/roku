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
	"net/url"
	"strings"
	"time"
)

const (
	Get        = HTTPMethod("GET")
	Post       = HTTPMethod("POST")
	Put        = HTTPMethod("PUT")
	Patch      = HTTPMethod("PATCH")
	Delete     = HTTPMethod("DELETE")
	ConTimeOut = 15 * time.Second
)

var (
	ErrTimeOut                  = rokuErr("timeout occurred")
	ErrMarshallValue            = rokuErr("failed to marshal the value to JSON")
	ErrBadRequest               = rokuErr("bad request")
	ErrNonPointerOrWrongCasting = rokuErr("value is not a pointer or casting type is incorrect")
	ErrEmptyItem                = rokuErr("item has no value and no error")
	ErrNilValue                 = rokuErr("nil value provided")
	ErrNilRequest               = rokuErr("nil request provided")
	ErrBadlyJSON                = rokuErr("badly-formed JSON in the body")
	ErrBadJSONType              = rokuErr("incorrect JSON type in the body")
	ErrEmptyBody                = rokuErr("body must not be empty")
	ErrBodyUnknownKey           = rokuErr("unknown key in the body")
	ErrBodySizeLimit            = rokuErr("body size limit exceeded")
	ErrBodyValue                = rokuErr("body must contain a single JSON value")
)

type (
	ReqI interface {
		Req()
	}

	ResI interface {
		Res()
	}

	ReqURLI interface {
		GetURLValues() url.Values
	}

	NoReq string

	NoRes string

	HTTPMethod string

	Envelope[T ResI] struct {
		Body *T
		*http.Response
	}

	rokuErr string

	ErrInvalidHTTPStatus struct {
		Res *http.Response
	}

	ErrDesc struct {
		StatusCode int    `json:"status_code,omitempty"`
		Status     string `json:"status,omitempty"`
		ErrMessage string `json:"error_message,omitempty"`
	}
)

func (nrq NoReq) Req() {}

func (nrp NoRes) Res() {}

func (r rokuErr) Error() string {
	return string(r)
}

func newResponse[T ResI](body *T, res *http.Response) *Envelope[T] {
	env := Envelope[T]{
		Body:     body,
		Response: res,
	}
	return &env
}

func newErrDesc(statusCode int, statusDesc string, message string) *ErrDesc {
	errParams := ErrDesc{StatusCode: statusCode, Status: statusDesc, ErrMessage: message}
	return &errParams
}

func (e ErrInvalidHTTPStatus) Error() string {
	if e.Res == nil {
		return ""
	}

	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			return
		}
	}(e.Res.Body)

	msg, err := io.ReadAll(e.Res.Body)
	if err != nil {
		return ""
	}

	errDesc := newErrDesc(e.Res.StatusCode, e.Res.Status, string(msg))
	errDescJSON, err := json.Marshal(errDesc)
	if err != nil {
		return ""
	}

	return string(errDescJSON)
}

func (e ErrInvalidHTTPStatus) UnmarshalError() (ErrDesc, error) {
	var errDesc ErrDesc

	reader := strings.NewReader(e.Error())
	err := ReadJSON(reader, &errDesc)
	if err != nil {
		return ErrDesc{}, err
	}

	return errDesc, nil
}

func GetErrorDesc(errHTTP ErrInvalidHTTPStatus) ErrDesc {
	errorDesc, err := errHTTP.UnmarshalError()
	if err != nil {
		return ErrDesc{}
	}

	return errorDesc
}

func NewHTTPClient(
	timeout time.Duration,
	redirectPolicy func(req *http.Request, via []*http.Request) error,
	transport http.RoundTripper,
) *http.Client {
	httpClient := http.Client{
		Timeout:       timeout,
		Transport:     transport,
		CheckRedirect: redirectPolicy,
	}
	return &httpClient
}

func FetchRx[T ReqI, U ResI](
	ctx context.Context,
	client *http.Client,
	method HTTPMethod,
	endpoint string,
	request *T,
	headers map[string]string,
	deadline time.Duration,
	backoffInterval time.Duration,
	backoffRetries uint64,
	statusCodeValidator ...func(res *http.Response) bool,
) rxgo.Observable {
	backOffCfg := backoff.NewExponentialBackOff()
	backOffCfg.InitialInterval = backoffInterval

	var validator func(res *http.Response) bool

	switch statusCodeValidator {
	case nil:
		validator = defaultInvalidStatusCodeValidator
	default:
		validator = statusCodeValidator[0]
	}

	return rxgo.Defer([]rxgo.Producer{
		func(_ context.Context, next chan<- rxgo.Item) {
			res, err := Fetch[T, U](
				ctx,
				client,
				method,
				endpoint,
				request,
				headers,
				deadline,
				validator,
			)
			if err != nil {
				next <- rxgo.Error(err)
			}
			next <- rxgo.Of(res)
		},
	},
	).BackOffRetry(
		backoff.WithMaxRetries(backOffCfg, backoffRetries),
	)
}

func Fetch[T ReqI, U ResI](
	ctx context.Context,
	client *http.Client,
	method HTTPMethod,
	endpoint string,
	request *T,
	headers map[string]string,
	deadline time.Duration,
	statusCodeValidator ...func(res *http.Response) bool,
) (*Envelope[U], error) {
	var body U
	var httpResponse *http.Response
	var data []byte
	var err error
	var validator func(res *http.Response) bool

	switch statusCodeValidator {
	case nil:
		validator = defaultInvalidStatusCodeValidator
	default:
		validator = statusCodeValidator[0]
	}

	reader, err := buildReader[T](request)
	if err != nil {
		return nil, err
	}

	switch reader {
	case nil:
		httpResponse, err = httpCall(ctx, client, endpoint, headers, nil, deadline, method, validator)
		if err != nil {
			return nil, err
		}
	default:
		httpResponse, err = httpCall(ctx, client, endpoint, headers, reader, deadline, method, validator)
		if err != nil {
			return nil, err
		}
	}

	if httpResponse.StatusCode != http.StatusNoContent {
		data, err = io.ReadAll(httpResponse.Body)
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
	method HTTPMethod,
	statusCodeValidator func(res *http.Response) bool,
) (*http.Response, error) {
	return do(ctx, client, url, method, headers, body, deadline, statusCodeValidator)
}

func do(
	ctx context.Context,
	client *http.Client,
	url string,
	method HTTPMethod,
	headers map[string]string,
	body io.Reader,
	deadline time.Duration,
	statusCodeValidator func(res *http.Response) bool,
) (*http.Response, error) {
	var res *http.Response
	var err error

	withCancelCtx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(deadline, func() {
		cancel()
	})

	defer func() {
		// Ensure the timer is stopped to avoid the cancel function being called unnecessarily
		if !timer.Stop() {
			select {
			case <-timer.C: // Drain the channel if the timer has already fired
			default:
			}
		}
	}()

	request, err := http.NewRequestWithContext(withCancelCtx, string(method), url, body)
	if err != nil {
		return nil, err
	}

	for k, v := range headers {
		request.Header.Add(k, v)
	}

	res, err = client.Do(request)
	if err != nil {

		if errors.Is(err, context.Canceled) {
			return nil, fmt.Errorf("service at %q %w", request.URL, ErrTimeOut)
		}
		return nil, err
	}

	if statusCodeValidator(res) {
		return nil, ErrInvalidHTTPStatus{Res: res}
	}

	return res, nil
}

// DefaultInvalidStatusCodeValidator validate all 4XX and 5XX error status codes
func defaultInvalidStatusCodeValidator(response *http.Response) bool {
	return (response.StatusCode/100)/4 == 1 || (response.StatusCode/100)/5 == 1
}

func toBytesReader[T any](value *T) (*bytes.Reader, error) {
	if value == nil {
		return nil, ErrNilValue
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%q: %w", err.Error(), ErrMarshallValue)
	}
	return bytes.NewReader(data), nil
}

func To[T any](item rxgo.Item) (*T, error) {
	switch item.V.(type) {
	case *T:
		return item.V.(*T), nil
	case nil:
		if item.E != nil {
			return nil, item.E
		}
		return nil, ErrEmptyItem
	case error:
		return nil, item.V.(error)
	default:
		return nil, ErrNonPointerOrWrongCasting
	}
}

func ReadJSON(body io.Reader, dst any) error {
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var (
			syntaxError         *json.SyntaxError
			unmarshalTypeError  *json.UnmarshalTypeError
			invalidUnmarshalErr *json.InvalidUnmarshalError
			maxBytesErr         *http.MaxBytesError
		)

		switch {
		case errors.As(err, &syntaxError):
			return fmt.Errorf("%w: at character %d", ErrBadlyJSON, syntaxError.Offset)
		case errors.Is(err, io.ErrUnexpectedEOF):
			return ErrBadlyJSON
		case errors.As(err, &unmarshalTypeError):
			if unmarshalTypeError.Field != "" {
				return fmt.Errorf("%w for field %q", ErrBadJSONType, unmarshalTypeError.Field)
			}
			return fmt.Errorf("%w at character %d", ErrBadJSONType, unmarshalTypeError.Offset)
		case errors.Is(err, io.EOF):
			return ErrEmptyBody
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			fieldName := strings.TrimPrefix(err.Error(), "json: unknown field ")
			return fmt.Errorf("%w %s", ErrBodyUnknownKey, fieldName)
		case errors.As(err, &maxBytesErr):
			return fmt.Errorf("%w. Max size is %d bytes", ErrBodySizeLimit, maxBytesErr.Limit)
		case errors.As(err, &invalidUnmarshalErr):
			panic(err)
		default:
			return err
		}
	}

	err := dec.Decode(&struct{}{})
	if !errors.Is(err, io.EOF) {
		return ErrBodyValue
	}

	return nil
}

func buildReader[T ReqI](request *T) (*bytes.Reader, error) {
	if reqURL, ok := any(request).(ReqURLI); ok {
		values := reqURL.GetURLValues()
		return bytes.NewReader([]byte(values.Encode())), nil
	}

	reader, err := toBytesReader[T](request)
	switch err {
	case nil:
		break
	default:
		if !errors.Is(err, ErrNilValue) {
			return nil, err
		}
	}

	return reader, nil
}
