package roku

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/go-cmp/cmp"
	"github.com/reactivex/rxgo/v2"
	"github.com/samber/lo"
	"github.com/v8tix/roku/middleware"
	"github.com/v8tix/roku/policy"
	"github.com/v8tix/roku/transport"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type (
	nonSerdeType string

	envelope map[string]any

	createUserV1Req struct {
		Name  string `json:"name,omitempty"`
		Email string `json:"email,omitempty"`
	}

	userResV struct {
		ID string `json:"id,omitempty"`
	}

	getUserV1Res struct {
		Name      string `json:"name,omitempty"`
		SmsNumber string `json:"sms_number,omitempty"`
		Enabled   bool   `json:"enabled,omitempty"`
	}

	getUserEnvV1Res struct {
		User getUserV1Res `json:"user,omitempty"`
	}
)

var (
	nonSerde = func() nonSerdeType {
		return ""
	}()
	badlyFormed = func() io.Reader {
		return strings.NewReader("{ \"name\" : \"Marco\"")
	}()
	linkHeader = func() map[string]string {
		return map[string]string{
			"Link": "<https://api.github.com/repositories/1300192/issues?page=2>; rel=\"prev\", <https://api.github.com/repositories/1300192/issues?page=4>; rel=\"next\", <https://api.github.com/repositories/1300192/issues?page=515>; rel=\"last\", <https://api.github.com/repositories/1300192/issues?page=1>; rel=\"first\"",
		}
	}()
	authorizationHeader = func() map[string]string {
		return map[string]string{
			"Authorization": "Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
		}
	}()
	headers = func() map[string]string {
		return lo.Assign[string, string](linkHeader, authorizationHeader)
	}()
	httpClient = func() *http.Client {
		return NewHTTPClient(
			5*time.Second,
			policy.OneRedirect,
			transport.IdleConnectionTimeout(ConnTimeOut),
		)
	}()
	customHeadersHTTPClient = func(headers middleware.CustomHeaders) *http.Client {
		return NewHTTPClient(
			5*time.Second,
			policy.OneRedirect,
			headers,
		)
	}
	userRes = func() getUserV1Res {
		return newGetUserV1Res("Marco", "1800-some-number", false)
	}()
	userEnvRes = func() *getUserEnvV1Res {
		return newGetUserEnvV1Res(userRes)
	}()
	cuReq = func() createUserV1Req {
		return newCreateUserV1Req("Adam Smith", "adam.smith@hotmail.com")
	}()
	cuReqReader = func() *bytes.Reader {
		b, _ := json.Marshal(cuReq)
		return bytes.NewReader(b)
	}
	cuRes = func() userResV {
		return newCreateUserRes("c9f9b69d-4321-40bb-bac9-3cb832648232")
	}()
	upsertUserSvr = func() *httptest.Server {
		return newTestServer(upsertUserHandler)
	}
	slowUserSvr = func() *httptest.Server {
		return newTestServer(slowUpsertUserHandler)
	}
	headerEchoSvr = func() *httptest.Server {
		return newTestServer(headerEchoHandler)
	}
	getUserSvr = func() *httptest.Server {
		return newTestServer(getUserHandler)
	}
	notFoundResSvr = func() *httptest.Server {
		return newTestServer(notFoundHandler)
	}
)

func (n *nonSerdeType) UnmarshalJSON(_ []byte) error {
	return errors.New("non-serde struct")
}

func (n nonSerdeType) MarshalJSON() ([]byte, error) {
	return nil, errors.New("non-serde struct")
}

func newCreateUserV1Req(name string, email string) createUserV1Req {
	return createUserV1Req{Name: name, Email: email}
}

func (createUserV1Req) Req() {}

func (userResV) Res() {}

func newGetUserV1Res(name string, smsNumber string, enabled bool) getUserV1Res {
	return getUserV1Res{Name: name, SmsNumber: smsNumber, Enabled: enabled}
}

func newGetUserEnvV1Res(user getUserV1Res) *getUserEnvV1Res {
	return &getUserEnvV1Res{User: user}
}

func (g getUserEnvV1Res) Res() {}

func newCreateUserRes(id string) userResV {
	return userResV{ID: id}
}

func newTestServer(handler func(http.ResponseWriter, *http.Request)) *httptest.Server {
	ts := httptest.NewServer(http.HandlerFunc(handler))
	return ts
}

func TestFetchingWithClientHeadersReturnsClientHeaders(t *testing.T) {
	t.Parallel()

	customClient := customHeadersHTTPClient(middleware.NewCustomHeaders(headers))
	ts := headerEchoSvr()
	defer ts.Close()

	cases := map[string]struct {
		client          *http.Client
		httpMethod      HTTPMethod
		url             string
		request         io.Reader
		headers         map[string]string
		deadline        time.Duration
		backoffInterval time.Duration
		backoffRetries  uint64
		cxt             context.Context
		want            map[string]string
	}{
		"with custom headers": {
			cxt:             context.Background(),
			client:          customClient,
			url:             ts.URL,
			request:         nil,
			headers:         nil,
			httpMethod:      Get,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            headers,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			resp, _, err := httpCall(
				context.Background(),
				tc.client,
				tc.url,
				tc.headers,
				tc.request,
				tc.deadline,
				Get,
				defaultInvalidStatusCodeValidator,
			)
			if err != nil {
				t.Fatal(err)
			}

			for k, v := range tc.want {
				if resp.Header.Get(k) != tc.want[k] {
					t.Fatalf("Expected header: %s:%s, Got: %s:%s", k, v, k, resp.Header.Get(k))
				}
			}
		})
	}
}

func TestFetchingWithContextHeadersReturnsContextHeaders(t *testing.T) {
	t.Parallel()

	ts := headerEchoSvr()
	defer ts.Close()

	cases := map[string]struct {
		client              *http.Client
		httpMethod          HTTPMethod
		url                 string
		request             io.Reader
		headers             map[string]string
		deadline            time.Duration
		backoffInterval     time.Duration
		backoffRetries      uint64
		statusCodeValidator func(res *http.Response) bool
		cxt                 context.Context
		want                map[string]string
	}{
		"with headers on context": {
			cxt:                 context.Background(),
			client:              httpClient,
			url:                 ts.URL,
			request:             nil,
			headers:             headers,
			httpMethod:          Get,
			deadline:            time.Second,
			backoffInterval:     150 * time.Millisecond,
			backoffRetries:      3,
			statusCodeValidator: defaultInvalidStatusCodeValidator,
			want:                headers,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			resp, _, err := httpCall(
				context.Background(),
				tc.client,
				tc.url,
				tc.headers,
				tc.request,
				tc.deadline,
				Get,
				defaultInvalidStatusCodeValidator,
			)
			if err != nil {
				t.Fatal(err)
			}

			for k, v := range tc.want {
				if resp.Header.Get(k) != tc.want[k] {
					t.Fatalf("Expected header: %s:%s, Got: %s:%s", k, v, k, resp.Header.Get(k))
				}
			}
		})
	}
}

func TestFetchingSlowServerReturnsContextCanceledError(t *testing.T) {
	t.Parallel()
	ts := slowUserSvr()
	defer ts.Close()

	cases := map[string]struct {
		client              *http.Client
		httpMethod          HTTPMethod
		url                 string
		request             io.Reader
		headers             map[string]string
		deadline            time.Duration
		backoffInterval     time.Duration
		backoffRetries      uint64
		statusCodeValidator func(res *http.Response) bool
		cxt                 context.Context
		want                error
	}{
		"with valid request": {
			cxt:                 context.Background(),
			client:              httpClient,
			url:                 ts.URL,
			request:             cuReqReader(),
			headers:             nil,
			httpMethod:          Post,
			deadline:            10 * time.Millisecond,
			backoffInterval:     150 * time.Millisecond,
			backoffRetries:      3,
			statusCodeValidator: defaultInvalidStatusCodeValidator,
			want:                ErrTimeOut,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			_, _, err := httpCall(
				context.Background(),
				tc.client,
				tc.url,
				tc.headers,
				tc.request,
				tc.deadline,
				tc.httpMethod,
				tc.statusCodeValidator,
			)
			switch err {
			case nil:
				t.Fatal("request didn't time out")
			default:
				if errors.Is(err, tc.want) {
					t.Log(err.Error())
				} else {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCastingWithValidValueReturnsValidValue(t *testing.T) {
	t.Parallel()

	var item rxgo.Item
	item.V = &cuRes

	cases := map[string]struct {
		input rxgo.Item
		want  *userResV
	}{
		"with valid value": {
			input: item,
			want:  &cuRes,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := To[userResV](tc.input)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCastingWithANonPointerValueReturnsError(t *testing.T) {
	t.Parallel()

	var item rxgo.Item
	item.V = cuRes

	cases := map[string]struct {
		input rxgo.Item
		want  error
	}{
		"with a non pointer value": {
			input: item,
			want:  ErrNonPointerOrWrongCasting,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := To[userResV](tc.input)
			if !errors.Is(err, tc.want) {
				t.Errorf("wrong error: %v", err)
			}
		})
	}
}

func TestCastingWithErrorValueReturnsError(t *testing.T) {
	t.Parallel()

	var item rxgo.Item
	item.V = ErrBadRequest

	cases := map[string]struct {
		input rxgo.Item
		want  error
	}{
		"with an empty item": {
			input: item,
			want:  ErrBadRequest,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := To[userResV](tc.input)
			if !errors.Is(err, tc.want) {
				t.Errorf("wrong error: %v", err)
			}
		})
	}
}

func TestCastingWithEmptyItemValueReturnsError(t *testing.T) {
	t.Parallel()

	var something interface{}
	var item rxgo.Item
	item.V = something

	cases := map[string]struct {
		input rxgo.Item
		want  error
	}{
		"with an empty item": {
			input: item,
			want:  ErrEmptyItem,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := To[userResV](tc.input)
			if !errors.Is(err, tc.want) {
				t.Errorf("wrong error: %v", err)
			}
		})
	}
}

func TestCastingWithNilValueAndNilErrorReturnsError(t *testing.T) {
	t.Parallel()

	var item rxgo.Item
	item.V = nil

	cases := map[string]struct {
		input rxgo.Item
		want  error
	}{
		"with nil value and nil error": {
			input: item,
			want:  ErrEmptyItem,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := To[userResV](tc.input)
			if !errors.Is(err, tc.want) {
				t.Errorf("wrong error: %v", err)
			}
		})
	}
}

func TestCastingWithNilValueAndErrorReturnsError(t *testing.T) {
	t.Parallel()

	operationError := errors.New("operation error")

	var item rxgo.Item
	item.V = nil
	item.E = operationError

	cases := map[string]struct {
		input rxgo.Item
		want  error
	}{
		"with nil value and an error": {
			input: item,
			want:  operationError,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := To[userResV](tc.input)
			if !errors.Is(err, tc.want) {
				t.Errorf("wrong error: %v", err)
			}
		})
	}
}

func TestCastingWithUnknownTypeValueReturnsErrUnknownType(t *testing.T) {
	t.Parallel()

	var item rxgo.Item
	item.V = &cuRes

	cases := map[string]struct {
		input rxgo.Item
		want  error
	}{
		"with unknown type": {
			input: item,
			want:  ErrNonPointerOrWrongCasting,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := To[createUserV1Req](tc.input)
			if !errors.Is(err, tc.want) {
				t.Errorf("wrong error: %v", err)
			}
		})
	}
}

func TestConvertingRequestToBytesReaderWithNilRequestReturnsError(t *testing.T) {
	t.Parallel()

	var req *createUserV1Req

	cases := map[string]struct {
		input *createUserV1Req
		want  error
	}{
		"with nil request": {
			input: req,
			want:  ErrNilValue,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := toBytesReader[createUserV1Req](tc.input)
			if !errors.Is(err, tc.want) {
				t.Errorf("wrong error: %v", err)
			}
		})
	}
}

func TestConvertingRequestToBytesReaderWithNonNilRequestReturnsReader(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		input *createUserV1Req
		want  error
	}{
		"with valid request": {
			input: &cuReq,
			want:  nil,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := toBytesReader[createUserV1Req](tc.input)
			if !errors.Is(err, tc.want) {
				t.Fatal(err)
			}
		})
	}
}

func TestConvertingRequestToBytesReaderWithNonSerdeTypeReturnsError(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		input nonSerdeType
		want  error
	}{
		"with non-serde type": {
			input: nonSerde,
			want:  ErrMarshallValue,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := toBytesReader[nonSerdeType](&tc.input)
			if !errors.Is(err, tc.want) {
				t.Errorf("wrong error: %v", err)
			}
		})
	}
}

func TestUnmarshallingRequestWithNilRequestReturnsError(t *testing.T) {
	t.Parallel()

	var req *http.Request

	cases := map[string]struct {
		input *http.Request
		want  error
	}{
		"with nil request": {
			input: req,
			want:  ErrNilValue,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := unmarshalReq[createUserV1Req](tc.input)
			if !errors.Is(err, tc.want) {
				t.Errorf("wrong error: %v", err)
			}
		})
	}
}

func TestReadJsonWithBadJsonReturnsError(t *testing.T) {
	t.Parallel()

	var req createUserV1Req

	cases := map[string]struct {
		input io.Reader
	}{
		"with bad json": {
			input: badlyFormed,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			err := ReadJSON(tc.input, &req)
			if err == nil {
				t.Errorf("error: %v", err)
			}
		})
	}
}

func TestFetchingWithNilBodyReturnsNonEmptyRes(t *testing.T) {
	t.Parallel()
	ts := getUserSvr()
	defer ts.Close()

	cases := map[string]struct {
		client          *http.Client
		httpMethod      HTTPMethod
		url             string
		request         *NoReq
		headers         map[string]string
		deadline        time.Duration
		backoffInterval time.Duration
		backoffRetries  uint64
		cxt             context.Context
		want            *getUserEnvV1Res
	}{
		"with get method": {
			cxt:             context.Background(),
			client:          httpClient,
			url:             ts.URL,
			request:         nil,
			headers:         nil,
			httpMethod:      Get,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            userEnvRes,
		},
		"with delete method": {
			cxt:             context.Background(),
			client:          httpClient,
			url:             ts.URL,
			request:         nil,
			headers:         nil,
			httpMethod:      Delete,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            userEnvRes,
		},
		"with post method": {
			cxt:             context.Background(),
			client:          httpClient,
			url:             ts.URL,
			request:         nil,
			headers:         nil,
			httpMethod:      Post,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            userEnvRes,
		},
		"with put method": {
			cxt:             context.Background(),
			client:          httpClient,
			url:             ts.URL,
			request:         nil,
			headers:         nil,
			httpMethod:      Put,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            userEnvRes,
		},
		"with patch method": {
			cxt:             context.Background(),
			client:          httpClient,
			url:             ts.URL,
			request:         nil,
			headers:         nil,
			httpMethod:      Patch,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            userEnvRes,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			ch := FetchRx[NoReq, getUserEnvV1Res](
				tc.cxt,
				tc.client,
				tc.httpMethod,
				ts.URL,
				tc.request,
				tc.headers,
				tc.deadline,
				tc.backoffInterval,
				tc.backoffRetries,
			).Observe()

			got, err := To[Envelope[getUserEnvV1Res]](<-ch)
			if err != nil {
				t.Fatal(err)
			}

			if !(cmp.Equal(tc.want, got.Body)) {
				t.Error(cmp.Diff(tc.want, got.Body))
			}
		})
	}
}

func TestFetchingWithNonNilBodyReturnsNonEmptyRes(t *testing.T) {
	t.Parallel()
	ts := upsertUserSvr()
	defer ts.Close()

	cases := map[string]struct {
		client          *http.Client
		httpMethod      HTTPMethod
		url             string
		request         createUserV1Req
		headers         map[string]string
		deadline        time.Duration
		backoffInterval time.Duration
		backoffRetries  uint64
		cxt             context.Context
		want            *getUserEnvV1Res
	}{
		"with post method": {
			cxt:             context.Background(),
			client:          httpClient,
			url:             ts.URL,
			request:         cuReq,
			headers:         nil,
			httpMethod:      Post,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            userEnvRes,
		},
		"with put method": {
			cxt:             context.Background(),
			client:          httpClient,
			url:             ts.URL,
			request:         cuReq,
			headers:         nil,
			httpMethod:      Put,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            userEnvRes,
		},
		"with patch method": {
			cxt:             context.Background(),
			client:          httpClient,
			url:             ts.URL,
			request:         cuReq,
			headers:         nil,
			httpMethod:      Patch,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            userEnvRes,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			ch := FetchRx[createUserV1Req, getUserEnvV1Res](
				tc.cxt,
				tc.client,
				tc.httpMethod,
				ts.URL,
				&tc.request,
				tc.headers,
				tc.deadline,
				tc.backoffInterval,
				tc.backoffRetries,
			).Observe()

			got, err := To[Envelope[getUserEnvV1Res]](<-ch)
			if err != nil {
				t.Fatal(err)
			}

			if !(cmp.Equal(tc.want, got.Body)) {
				t.Error(cmp.Diff(tc.want, got.Body))
			}
		})
	}
}

func TestFetchingWithNilBodyReturnsNotFoundErr(t *testing.T) {
	t.Parallel()
	ts := notFoundResSvr()
	defer ts.Close()

	cases := map[string]struct {
		client          *http.Client
		httpMethod      HTTPMethod
		url             string
		request         *NoReq
		headers         map[string]string
		deadline        time.Duration
		backoffInterval time.Duration
		backoffRetries  uint64
		cxt             context.Context
		want            *NoRes
	}{
		"with get method": {
			cxt:             context.Background(),
			client:          httpClient,
			url:             ts.URL,
			request:         nil,
			headers:         nil,
			httpMethod:      Get,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            nil,
		},
		"with delete method": {
			cxt:             context.Background(),
			client:          httpClient,
			url:             ts.URL,
			request:         nil,
			headers:         nil,
			httpMethod:      Delete,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            nil,
		},
		"with post method": {
			cxt:             context.Background(),
			client:          httpClient,
			url:             ts.URL,
			request:         nil,
			headers:         nil,
			httpMethod:      Post,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            nil,
		},
		"with put method": {
			cxt:             context.Background(),
			client:          httpClient,
			url:             ts.URL,
			request:         nil,
			headers:         nil,
			httpMethod:      Put,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            nil,
		},
		"with patch method": {
			cxt:             context.Background(),
			client:          httpClient,
			url:             ts.URL,
			request:         nil,
			headers:         nil,
			httpMethod:      Patch,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            nil,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			ch := FetchRx[NoReq, getUserEnvV1Res](
				tc.cxt,
				tc.client,
				tc.httpMethod,
				ts.URL,
				tc.request,
				tc.headers,
				tc.deadline,
				tc.backoffInterval,
				tc.backoffRetries,
			).Observe()

			got, err := To[Envelope[getUserEnvV1Res]](<-ch)
			switch err {
			case nil:
				t.Fatal(err)
			default:
				if got != nil {
					t.Fatal(errors.New("envelop is not nil"))
				}

				if errors.As(err, &ErrInvalidHTTPStatus{}) {
					errDesc := GetErrorDesc(err)
					if errDesc.StatusCode != http.StatusNotFound {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func TestFetchingWithNonNilBodyReturnsNotFoundErr(t *testing.T) {
	t.Parallel()
	ts := notFoundResSvr()
	defer ts.Close()

	cases := map[string]struct {
		client          *http.Client
		httpMethod      HTTPMethod
		url             string
		request         *createUserV1Req
		headers         map[string]string
		deadline        time.Duration
		backoffInterval time.Duration
		backoffRetries  uint64
		cxt             context.Context
		want            *NoRes
	}{
		"with get method": {
			cxt:             context.Background(),
			client:          httpClient,
			url:             ts.URL,
			request:         &cuReq,
			headers:         nil,
			httpMethod:      Get,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            nil,
		},
		"with delete method": {
			cxt:             context.Background(),
			client:          httpClient,
			url:             ts.URL,
			request:         &cuReq,
			headers:         nil,
			httpMethod:      Delete,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            nil,
		},
		"with post method": {
			cxt:             context.Background(),
			client:          httpClient,
			url:             ts.URL,
			request:         &cuReq,
			headers:         nil,
			httpMethod:      Post,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            nil,
		},
		"with put method": {
			cxt:             context.Background(),
			client:          httpClient,
			url:             ts.URL,
			request:         &cuReq,
			headers:         nil,
			httpMethod:      Put,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            nil,
		},
		"with patch method": {
			cxt:             context.Background(),
			client:          httpClient,
			url:             ts.URL,
			request:         &cuReq,
			headers:         nil,
			httpMethod:      Patch,
			deadline:        time.Second,
			backoffInterval: 150 * time.Millisecond,
			backoffRetries:  3,
			want:            nil,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			ch := FetchRx[createUserV1Req, getUserEnvV1Res](
				tc.cxt,
				tc.client,
				tc.httpMethod,
				ts.URL,
				tc.request,
				tc.headers,
				tc.deadline,
				tc.backoffInterval,
				tc.backoffRetries,
			).Observe()

			got, err := To[Envelope[getUserEnvV1Res]](<-ch)
			switch err {
			case nil:
				t.Fatal(err)
			default:
				if got != nil {
					t.Fatal(errors.New("envelop is not nil"))
				}

				if errors.As(err, &ErrInvalidHTTPStatus{}) {
					errProps := GetErrorDesc(err)
					if errProps.StatusCode != http.StatusNotFound {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func getUserHandler(w http.ResponseWriter, r *http.Request) {
	env := envelope{
		"user": userRes,
	}

	err := write(w, http.StatusOK, env, nil)
	if err != nil {
		serverErrorResponse(w, r)
	}
}

func headerEchoHandler(w http.ResponseWriter, r *http.Request) {
	for k, v := range r.Header {
		w.Header().Set(k, v[0])
	}

	env := envelope{
		"user": userRes,
	}

	err := write(w, http.StatusOK, env, nil)
	if err != nil {
		serverErrorResponse(w, r)
	}
}

func slowUpsertUserHandler(w http.ResponseWriter, r *http.Request) {
	time.Sleep(500 * time.Millisecond)
	upsertUserHandler(w, r)
}

func upsertUserHandler(w http.ResponseWriter, r *http.Request) {
	maxBytes := 1_048_576
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxBytes))

	var req createUserV1Req

	err := ReadJSON(r.Body, &req)
	if err != nil {
		errorResponse(w, r, http.StatusBadRequest, err.Error())
		return
	}

	env := envelope{
		"user": userRes,
	}

	err = write(w, http.StatusOK, env, nil)
	if err != nil {
		serverErrorResponse(w, r)
	}
}

func notFoundHandler(w http.ResponseWriter, r *http.Request) {
	errorResponse(w, r, http.StatusNotFound, errors.New("user not found"))
}

func unmarshalReq[T ReqI](req *http.Request) (*T, error) {
	var genReq T

	if req == nil {
		return nil, ErrNilValue
	}

	err := ReadJSON(req.Body, &genReq)
	if err != nil {
		return nil, fmt.Errorf("%q: %w", err.Error(), ErrBadRequest)
	}

	return &genReq, nil
}

func errorResponse(w http.ResponseWriter, _ *http.Request, status int, message any) {
	env := envelope{"error": message}

	err := write(w, status, env, nil)
	if err != nil {
		w.WriteHeader(500)
	}
}

func write(w http.ResponseWriter, status int, data envelope, headers http.Header) error {
	js, err := json.MarshalIndent(data, "", "\t")
	if err != nil {
		return err
	}

	js = append(js, '\n')

	for key, value := range headers {
		w.Header()[key] = value
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(js)

	return nil
}

func serverErrorResponse(w http.ResponseWriter, r *http.Request) {
	message := "the server encountered a problem and could not process your request"
	errorResponse(w, r, http.StatusInternalServerError, message)
}
