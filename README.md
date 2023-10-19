# Roku

## Overview

The Roku REST client provides a straightforward and simple interface for interacting with REST APIs. With Roku you can:

* Easily marshal and unmarshal requests and responses without the need for boilerplate or repetitive code.
* Utilize the built-in exponential backoff algorithm.
* Enhance your request functions by wrapping them with [RxGo 2](https://github.com/ReactiveX/RxGo) Observable streams, enabling the creation of custom pipelines, including parallel requests.

## Table of Contents

- [Usage](#usage)
- [Contributing](#contributing)
- [License](#license)

### Usage.

* Before starting to make requests, we need to create a *http.Client in Golang. For this, we can use Roku's constructor
  method:
  ````
  import (
    ...
    "github.com/v8tix/roku"
    "net/http"
    ...
  )
  
  func NewHTTPClient(
    timeout time.Duration,
    redirectPolicy func(req *http.Request, via []*http.Request) error,
    transport http.RoundTripper,
  ) *http.Client {
  ...
  }
  ```` 
  
  * Where:<br>
  <br>**timeout**: Its purpose is to control how long the client waits to receive a response from the server before considering the request as failed due to a timeout.<br>
  <br>**redirectPolicy**: Its purpose is to allow you to control how the HTTP client handles 3xx redirection responses.<br>
  <br>**transport**: Its purpose is to allow you to have more granular control over how HTTP requests are made and how network connections are managed.<br>
  
  * Example:<br>
  ````
  NewHTTPClient(
  	5*time.Second,
  	policy.OneRedirect,
  	transport.IdleConnectionTimeout(ConnTimeOut),
  )
  ```` 
  <br>The OneRedirect and IdleConnectionTimeout functions are examples of how the Golang REST client can be configured and are part of Roku.<br>    

*Once our client is configured, we can start configuring the functions provided by Roku: Fetch and FetchRx. The difference between them is that Fetch does not provide a backoff mechanism and does not return the response as an observable. For production environments, we recommend using FetchRx.

  * FetchRx signature:<br>
````
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
  statusCodeValidator func(res *http.Response) bool,
) rxgo.Observable {
  ...
}
````
  * FetchRx example:<br>

````
  ch := FetchRxFetchRx[CreateUserV1Req, GetUserEnvV1Res](
          context.Background(),
          client,
          roku.Put,
          url,
          createUserV1Req,
          nil,
          time.Second,
          150 * time.Millisecond,
          3,
          roku.DefaultInvalidStatusCodeValidator,
  ).Observe()

  res, err := roku.To[roku.Envelope[GetUserEnvV1Res]](<-ch)
  switch err {
    case nil:
      if &res.Body.User != nil {
        ...  
        return res.Body.User
      }
    default:
      if errors.As(err, &ErrInvalidHTTPStatus{}) {
        errDesc := roku.GetErrorDesc(err)
        return fmt.Errorf("http error: %v", errDesc)      
      }
  }
```` 
* Notes about the example:<br>
  * Due to generics in Golang, CreateUserV1Req and GetUserEnvV1Res must implement the roku.ReqI and roku.ResI interfaces, respectively.
````
  type CreateUserV1Req struct {
		Name  string `json:"name,omitempty"`
		Email string `json:"email,omitempty"`
  }
  
  func (CreateUserV1Req) Req() {}
  
  type GetUserEnvV1Res struct {
    User GetUserV1Res `json:"user,omitempty"`
  }
  
  func (GetUserEnvV1Res) Res() {}
```` 
  * In cases where the request does not have a request body, you should use the type roku.NoReq and pass nil as a parameter to FetchRx.
  * In cases where the request does not return a response, you should use the type roku.NoRes for FetchRx.
````
  ch:= roku.FetchRx[roku.NoReq, roku.NoRes](
    ctx,
    r.client,
    roku.Delete,
    url,
    nil,
    nil,
    roku.DeadLine,
    roku.RetryInterval,
    3,
    roku.DefaultInvalidStatusCodeValidator,
  ).Observe()

  _, err := roku.To[roku.Envelope[roku.NoRes]](<-ch)
  switch err {
    case nil:
      return nil
    default:
      if errors.As(err, &roku.ErrInvalidHttpStatus{}) {
        errDesc := roku.GetErrorDesc(err)(err)
        return fmt.Errorf("http error: %v", errDesc)
      }
  }
```` 
  * The DefaultInvalidStatusCodeValidator function is a utility that allows you to check for error status codes in the 4xx and 5xx range of responses. The error it returns is of type ErrInvalidHTTPStatus. The latter is a wrapper for a pointer of type *http.Response.<br>
    <br>
  * **deadline**: It enables FetchRx to block requests to the service, preventing both overloading and cascading failures.<br>
    <br>
  * **backoffInterval**: It represents a waiting period in which FetchRx delays before attempting to retry a failed request.<br>
      <br>
  * **backoffRetries**: It represents the maximum number of retry attempts that FetchRx will make for a request.<br>
      <br>

### Contributing.

1. Fork the repository
2. Clone the forked repository
3. Create a new branch for your feature or fix
4. Make your changes
5. Submit a pull request

### License.

This project is licensed under the [MIT License](https://opensource.org/license/mit/). You can refer to the [LICENSE](LICENSE) file for more information.
