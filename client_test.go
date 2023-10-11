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
	"testing"
	"time"
)

type nonSerdeType string

func (n *nonSerdeType) UnmarshalJSON(_ []byte) error {
	return errors.New("non-serde struct")
}

func (n nonSerdeType) MarshalJSON() ([]byte, error) {
	return nil, errors.New("non-serde struct")
}

type createUserReq struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

func (createUserReq) Req() {}

type simpleRes struct {
	Msg string
}

func (simpleRes) Res() {}

type createUserRes struct {
	ID string `json:"id,omitempty"`
}

func (createUserRes) Res() {}

func newCreateUserReq(name, email string) createUserReq {
	return createUserReq{
		Name:  name,
		Email: email,
	}
}

func newCreateUserRes(id string) createUserRes {
	return createUserRes{ID: id}
}

func newTestServer(handler func(http.ResponseWriter, *http.Request)) *httptest.Server {
	ts := httptest.NewServer(http.HandlerFunc(handler))
	return ts
}

var (
	ErrInternalServer = errors.New("internal server error")
	nonSerde          = func() nonSerdeType {
		return ""
	}()
	badlyFormed = func() io.Reader {
		data := []byte("{ \"name\" : \"Marco\"")
		return bytes.NewReader(data)
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
			RetryInterval,
			policy.OneRedirect,
			transport.IdleConnectionTimeout(ConnTimeOut),
		)
	}()
	customHeadersHttpClient = func(headers middleware.CustomHeaders) *http.Client {
		return NewHTTPClient(
			RetryInterval,
			policy.OneRedirect,
			headers,
		)
	}
	sm = func() simpleRes {
		return simpleRes{Msg: "Hello World !"}
	}()
	emptyRes = func() *EmptyRes {
		return nil
	}()
	cuReq = func() createUserReq {
		return newCreateUserReq("Adam Smith", "adam.smith@hotmail.com")
	}()
	cuReqReader = func() *bytes.Reader {
		b, _ := json.Marshal(cuReq)
		return bytes.NewReader(b)
	}
	cuRes = func() createUserRes {
		return newCreateUserRes("c9f9b69d-4321-40bb-bac9-3cb832648232")
	}()
	getSvr = func() *httptest.Server {
		return newTestServer(genericGetHandler[simpleRes](&sm))
	}
	deleteNoContentSvr = func() *httptest.Server {
		return newTestServer(genericDeleteHandler[simpleRes](&sm))
	}
	upsertSvr = func() *httptest.Server {
		return newTestServer(
			genericUpsertHandler[createUserReq, createUserRes](
				cuReq,
				cuRes,
				fixedReqValidator[createUserReq](),
			),
		)
	}
	userEmptyReqSvr = func() *httptest.Server {
		var er createUserReq
		return newTestServer(
			genericUpsertHandler[createUserReq, createUserRes](
				er,
				cuRes,
				reqValidator[createUserReq](),
			),
		)
	}
	slowSvr = func() *httptest.Server {
		return newTestServer(
			genericUpsertHandler[createUserReq, createUserRes](
				cuReq,
				cuRes,
				slowReqValidator[createUserReq](),
			),
		)
	}
	//Header Middleware
	headerEchoSvr = func() *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for k, v := range r.Header {
				w.Header().Set(k, v[0])
			}
			_, err := fmt.Fprint(w, sm.Msg)
			if err != nil {
				return
			}
		}))
	}
)

func TestAddHeaderMiddleware(t *testing.T) {
	t.Parallel()

	customClient := customHeadersHttpClient(middleware.NewCustomHeaders(linkHeader))
	ts := headerEchoSvr()
	defer ts.Close()

	cases := map[string]struct {
		input map[string]string
		want  simpleRes
	}{
		"with custom headers round tripper": {
			input: authorizationHeader,
			want:  sm,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			resp, _, err := httpGet(
				context.Background(),
				customClient,
				ts.URL,
				tc.input,
				200*time.Millisecond,
				DefaultInvalidStatusCodeValidator,
			)
			if err != nil {
				t.Fatal(err)
			}

			for k, v := range headers {
				if resp.Header.Get(k) != headers[k] {
					t.Fatalf("Expected header: %s:%s, Got: %s:%s", k, v, k, resp.Header.Get(k))
				}
			}
		})
	}
}

// Header Context
func TestAddHeaderContext(t *testing.T) {
	t.Parallel()

	ts := headerEchoSvr()
	defer ts.Close()

	resp, _, err := httpGet(
		context.Background(),
		httpClient,
		ts.URL,
		linkHeader,
		200*time.Millisecond,
		DefaultInvalidStatusCodeValidator,
	)
	if err != nil {
		t.Fatalf("Expected non-nil error, got: %v", err)
	}

	for k, v := range linkHeader {
		if resp.Header.Get(k) != linkHeader[k] {
			t.Fatalf("Expected header: %s:%s, Got: %s:%s", k, v, k, linkHeader[k])
		}
	}
}

func TestFetchingSlowServerWithHttpPostReturnsContextCanceledError(t *testing.T) {
	t.Parallel()
	ts := slowSvr()
	defer ts.Close()

	cases := map[string]struct {
		input *bytes.Reader
		want  error
	}{
		"with valid request": {
			input: cuReqReader(),
			want:  nil,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			_, _, err := httpUpsert(
				context.Background(),
				httpClient,
				ts.URL,
				nil,
				tc.input,
				200*time.Millisecond,
				Post,
				DefaultInvalidStatusCodeValidator,
			)
			switch err {
			case nil:
				t.Fatal("request didn't time out")
			default:
				if errors.Is(err, ErrTimeOut) {
					t.Log(err.Error())
				} else {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestFetchingWithGetMethodReturnsHelloWorld(t *testing.T) {
	t.Parallel()
	ts := getSvr()
	defer ts.Close()

	cases := map[string]struct {
		want *simpleRes
	}{
		"with get method": {
			want: &sm,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			got, err := Fetch[EmptyReq, simpleRes](
				context.Background(),
				httpClient,
				Get,
				ts.URL,
				nil,
				nil,
				RetryInterval,
				DefaultInvalidStatusCodeValidator,
			)
			if err != nil {
				t.Fatal(err)
			}

			if !(cmp.Equal(tc.want, got.UnmarshalledBody)) {
				t.Error(cmp.Diff(sm, got.UnmarshalledBody))
			}
		})
	}
}

func TestFetchingWithDeleteMethodReturnsEmptyResponse(t *testing.T) {
	t.Parallel()
	ts := deleteNoContentSvr()
	defer ts.Close()

	cases := map[string]struct {
		want *EmptyRes
	}{
		"with delete method": {
			want: emptyRes,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			got, err := Fetch[EmptyReq, EmptyRes](
				context.Background(),
				httpClient,
				Delete,
				ts.URL,
				nil,
				nil,
				RetryInterval,
				DefaultInvalidStatusCodeValidator,
			)
			if err != nil {
				t.Fatal(err)
			}

			if !(cmp.Equal(tc.want, got.UnmarshalledBody)) {
				t.Error(cmp.Diff(sm, got.UnmarshalledBody))
			}
		})
	}
}

func TestFetchingWithRxGetMethodReturnsHelloWorld(t *testing.T) {
	t.Parallel()
	ts := getSvr()
	defer ts.Close()

	cases := map[string]struct {
		want *simpleRes
	}{
		"with get method": {
			want: &sm,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			ch := FetchRx[EmptyReq, simpleRes](
				context.Background(),
				httpClient,
				Get,
				ts.URL,
				nil,
				nil,
				DeadLine,
				RetryInterval,
				1,
				DefaultInvalidStatusCodeValidator,
			).Observe()

			got, err := To[HttpResponse[simpleRes]](<-ch)
			if err != nil {
				t.Fatal(err)
			}

			if !(cmp.Equal(tc.want, got.UnmarshalledBody)) {
				t.Error(cmp.Diff(sm, got.UnmarshalledBody))
			}
		})
	}
}

func TestFetchingWithGetMethodWithInvalidMethodReturnsError(t *testing.T) {
	t.Parallel()
	ts := getSvr()
	url := ts.URL
	defer ts.Close()

	cases := map[string]struct {
		input *bytes.Reader
		want  error
	}{
		"with invalid method": {
			input: cuReqReader(),
			want:  nil,
		},
	}

	for input := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := Fetch[EmptyReq, simpleRes](
				context.Background(),
				httpClient,
				Post,
				url,
				nil,
				nil,
				RetryInterval,
				DefaultInvalidStatusCodeValidator,
			)
			if err == nil {
				t.Fatal(err)
			}
		})
	}
}

func TestFetchingWithHttpPostMethodReturnsCreateUserRes(t *testing.T) {
	t.Parallel()
	var got createUserRes
	ts := upsertSvr()
	defer ts.Close()

	cases := map[string]struct {
		input *bytes.Reader
		want  error
	}{
		"with valid request": {
			input: cuReqReader(),
			want:  nil,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			resp, timer, err := httpUpsert(
				context.Background(),
				httpClient,
				ts.URL,
				nil,
				tc.input,
				DeadLine,
				Post,
				DefaultInvalidStatusCodeValidator,
			)
			if err != nil {
				t.Fatal(err)
			}

			data, err := readBody(timer, resp.Body)
			if err != nil {
				t.Fatal(err)
			}

			err = json.Unmarshal(data, &got)
			if err != nil {
				t.Fatal(err)
			}

			if !cmp.Equal(got, cuRes) {
				t.Error(cmp.Diff(got, cuRes))
			}
		})
	}
}

func TestFetchingWithHttpGetMethodReturnsErrInvalidHttpStatus(t *testing.T) {
	t.Parallel()
	ts := upsertSvr()
	defer ts.Close()

	cases := map[string]struct {
		input HttpMethod
		want  int
	}{
		"with get method": {
			input: Get,
			want:  http.StatusMethodNotAllowed,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := Fetch[EmptyReq, simpleRes](
				context.Background(),
				httpClient,
				tc.input,
				ts.URL,
				nil,
				nil,
				RetryInterval,
				DefaultInvalidStatusCodeValidator,
			)
			switch err {
			case nil:
				t.Fatal(err)
			default:

				if !errors.As(err, &ErrInvalidHttpStatus{}) {
					t.Fatal(err)
				}

				errorProperties := GetErrorProperties(err)

				if errorProperties.StatusCode != tc.want {
					t.Fatal("invalid status code")
				}

				break
			}
		})
	}
}

func TestFetchingWithHttpPostMethodAndBadReqReturnsBadRequest(t *testing.T) {
	t.Parallel()
	ts := userEmptyReqSvr()
	defer ts.Close()

	cases := map[string]struct {
		input *bytes.Reader
		want  error
	}{
		"with bad request": {
			input: cuReqReader(),
			want:  nil,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			_, _, err := httpUpsert(
				context.Background(),
				httpClient,
				ts.URL,
				nil,
				tc.input,
				RetryInterval,
				Post,
				DefaultInvalidStatusCodeValidator,
			)
			if err == tc.want {
				t.Fatal(err)
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
		want  *createUserRes
	}{
		"with valid value": {
			input: item,
			want:  &cuRes,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := To[createUserRes](tc.input)
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
			_, err := To[createUserRes](tc.input)
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
			_, err := To[createUserRes](tc.input)
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
			_, err := To[createUserRes](tc.input)
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
			_, err := To[createUserRes](tc.input)
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
			_, err := To[createUserRes](tc.input)
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
			_, err := To[createUserReq](tc.input)
			if !errors.Is(err, tc.want) {
				t.Errorf("wrong error: %v", err)
			}
		})
	}
}

func TestReadingBodyWithNonResponseReturnsData(t *testing.T) {
	t.Parallel()
	ts := getSvr()
	defer ts.Close()

	cases := map[string]struct {
		input *httptest.Server
		want  simpleRes
	}{
		"with get method": {
			input: ts,
			want:  sm,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			got, timer, err := httpGet(
				context.Background(),
				httpClient,
				tc.input.URL,
				nil,
				RetryInterval,
				DefaultInvalidStatusCodeValidator,
			)
			if err != nil {
				t.Fatal(err)
			}

			_, err = readBody(timer, got.Body)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestConvertingRequestToBytesReaderWithNilRequestReturnsError(t *testing.T) {
	t.Parallel()

	var req *createUserReq

	cases := map[string]struct {
		input *createUserReq
		want  error
	}{
		"with nil request": {
			input: req,
			want:  ErrNilValue,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := toBytesReader[createUserReq](tc.input)
			if !errors.Is(err, tc.want) {
				t.Errorf("wrong error: %v", err)
			}
		})
	}
}

func TestConvertingRequestToBytesReaderWithNonNilRequestReturnsReader(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		input *createUserReq
		want  error
	}{
		"with valid request": {
			input: &cuReq,
			want:  nil,
		},
	}

	for input, tc := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := toBytesReader[createUserReq](tc.input)
			if err != tc.want {
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
			_, err := unmarshalReq[createUserReq](tc.input)
			if !errors.Is(err, tc.want) {
				t.Errorf("wrong error: %v", err)
			}
		})
	}
}

func TestReadJsonWithBadJsonReturnsError(t *testing.T) {
	t.Parallel()

	var req createUserReq

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

func reqValidator[T any]() func(req1 T, req2 T) bool {
	return func(req1 T, req2 T) bool {
		return cmp.Equal(req1, req2)
	}
}

func fixedReqValidator[T any]() func(req1 T, req2 T) bool {
	return func(req1 T, req2 T) bool {
		return true
	}
}

func slowReqValidator[T any]() func(req1 T, req2 T) bool {
	return func(req1 T, req2 T) bool {
		time.Sleep(500 * time.Millisecond)
		return true
	}
}

func genericGetHandler[T any](resp *T) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == string(Get) {
			err := writeJSON[T](w, http.StatusOK, resp, nil)
			if err != nil {
				http.Error(w, "error writing data", http.StatusInternalServerError)
				return
			}
			return
		} else {
			http.Error(w, "invalid HTTP method specified", http.StatusMethodNotAllowed)
			return
		}
	}
}

func genericDeleteHandler[T any](resp *T) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == string(Delete) {
			err := writeJSON[T](w, http.StatusNoContent, resp, nil)
			if err != nil {
				http.Error(w, "error writing data", http.StatusInternalServerError)
				return
			}
			return
		} else {
			http.Error(w, "invalid HTTP method specified", http.StatusMethodNotAllowed)
			return
		}
	}
}

func genericUpsertHandler[T ReqI, U ResI](
	req T,
	res U,
	f func(T, T) bool,
) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == string(Post) || r.Method == string(Put) || r.Method == string(Patch) {
			reqT, err := unmarshalReq[T](r)
			if err != nil {
				switch {
				case errors.Is(err, ErrInternalServer):
					http.Error(w, "UnmarshalReq", http.StatusInternalServerError)
					return
				case errors.Is(err, ErrBadRequest):
					http.Error(w, "UnmarshalReq", http.StatusBadRequest)
					return
				default:
					http.Error(w, "UnmarshalReq", http.StatusInternalServerError)
					return
				}
			}

			if !f(*reqT, req) {
				http.Error(w, "requests are not equal", http.StatusBadRequest)
				return
			}

			err = writeJSON[U](w, http.StatusOK, &res, nil)
			if err != nil {
				http.Error(w, "error writing data", http.StatusInternalServerError)
				return
			}
			return
		}

		http.Error(w, "Invalid HTTP method specified", http.StatusMethodNotAllowed)
		return
	}
}

func writeJSON[U any](w http.ResponseWriter, status int, data *U, headers http.Header) error {
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
	w.Write(js)

	return nil
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
