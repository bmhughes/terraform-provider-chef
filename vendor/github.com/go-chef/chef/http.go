package chef

import (
	"bytes"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// ChefVersion that we pretend to emulate
const ChefVersion = "14.0.0"

// Body wraps io.Reader and adds methods for calculating hashes and detecting content
type Body struct {
	io.Reader
}

// AuthConfig representing a client and a private key used for encryption
//  This is embedded in the Client type
type AuthConfig struct {
	PrivateKey            *rsa.PrivateKey
	ClientName            string
	AuthenticationVersion string
}

// Client is vessel for public methods used against the chef-server
type Client struct {
	Auth       *AuthConfig
	BaseURL    *url.URL
	client     *http.Client
	IsWebuiKey bool

	ACLs              *ACLService
	Associations      *AssociationService
	AuthenticateUser  *AuthenticateUserService
	Clients           *ApiClientService
	Containers        *ContainerService
	CookbookArtifacts *CBAService
	Cookbooks         *CookbookService
	DataBags          *DataBagService
	Environments      *EnvironmentService
	Groups            *GroupService
	License           *LicenseService
	Nodes             *NodeService
	Organizations     *OrganizationService
	Policies          *PolicyService
	PolicyGroups      *PolicyGroupService
	Principals        *PrincipalService
	RequiredRecipe    *RequiredRecipeService
	Roles             *RoleService
	Sandboxes         *SandboxService
	Search            *SearchService
	Stats             *StatsService
	Status            *StatusService
	Universe          *UniverseService
	UpdatedSince      *UpdatedSinceService
	Users             *UserService
}

// Config contains the configuration options for a chef client. This structure is used primarily in the NewClient() constructor in order to setup a proper client object
type Config struct {
	// This should be the user ID on the chef server
	Name string

	// This is the plain text private Key for the user
	Key string

	// BaseURL is the chef server URL used to connect to. If using orgs you should include your org in the url and terminate the url with a "/"
	BaseURL string

	// When set to false (default) this will enable SSL Cert Verification. If you need to disable Cert Verification set to true
	SkipSSL bool

	// RootCAs is a reference to x509.CertPool for TLS
	RootCAs *x509.CertPool

	// Time to wait in seconds before giving up on a request to the server
	Timeout int

	// Authentication Protocol Version
	AuthenticationVersion string

	// When set to true corresponding API is using webui key in the request
	IsWebuiKey bool

	// Proxy function to be used when making requests
	Proxy func(*http.Request) (*url.URL, error)
}

/*
An ErrorResponse reports one or more errors caused by an API request.
Thanks to https://github.com/google/go-github

The Response structure includes:
        Status string
	StatusCode int
*/
type ErrorResponse struct {
	Response *http.Response // HTTP response that caused this error
	// extracted error message converted to string if possible
	ErrorMsg string
	// json body raw byte stream from an error
	ErrorText []byte
}

type ErrorMsg struct {
	Error interface{} `json:"error"`
}

// Buffer creates a  byte.Buffer copy from a io.Reader resets read on reader to 0,0
func (body *Body) Buffer() *bytes.Buffer {
	var b bytes.Buffer
	if body.Reader == nil {
		return &b
	}

	b.ReadFrom(body.Reader)
	_, err := body.Reader.(io.Seeker).Seek(0, 0)
	if err != nil {
		log.Fatal(err)
	}
	return &b
}

// Hash calculates the body content hash
func (body *Body) Hash() (h string) {
	b := body.Buffer()
	// empty buffs should return a empty string
	if b.Len() == 0 {
		h = HashStr("")
	}
	h = HashStr(b.String())
	return
}

// Hash256 calculates the body content hash
func (body *Body) Hash256() (h string) {
	b := body.Buffer()
	// empty buffs should return a empty string
	if b.Len() == 0 {
		h = HashStr256("")
	}
	h = HashStr256(b.String())
	return
}

// ContentType returns the content-type string of Body as detected by http.DetectContentType()
func (body *Body) ContentType() string {
	if json.Unmarshal(body.Buffer().Bytes(), &struct{}{}) == nil {
		return "application/json"
	}
	return http.DetectContentType(body.Buffer().Bytes())
}

// Error implements the error interface method for ErrorResponse
func (r *ErrorResponse) Error() string {
	return fmt.Sprintf("%v %v: %d",
		r.Response.Request.Method, r.Response.Request.URL,
		r.Response.StatusCode)
}

// StatusCode returns the status code from the http response embedded in the ErrorResponse
func (r *ErrorResponse) StatusCode() int {
	return r.Response.StatusCode
}

// StatusMsg returns the error msg string from the http response. The message is a best
// effort value and depends on the Chef Server json return format
func (r *ErrorResponse) StatusMsg() string {
	return r.ErrorMsg
}

// StatusText returns the raw json response included in the http response
func (r *ErrorResponse) StatusText() []byte {
	return r.ErrorText
}

// StatusMethod returns the method used from the http response embedded in the ErrorResponse
func (r *ErrorResponse) StatusMethod() string {
	return r.Response.Request.Method
}

// StatusURL returns the URL used from the http response embedded in the ErrorResponse
func (r *ErrorResponse) StatusURL() *url.URL {
	return r.Response.Request.URL
}

// NewClient is the client generator used to instantiate a client for talking to a chef-server
// It is a simple constructor for the Client struct intended as a easy interface for issuing
// signed requests
func NewClient(cfg *Config) (*Client, error) {

	// Verify Config settings
	// Authentication version = 1.0 or 1.3, default to 1.0
	cfg.VerifyVersion()

	pk, err := PrivateKeyFromString([]byte(cfg.Key))
	if err != nil {
		return nil, err
	}

	baseUrl, _ := url.Parse(cfg.BaseURL)

	tlsConfig := &tls.Config{InsecureSkipVerify: cfg.SkipSSL}
	if cfg.RootCAs != nil {
		tlsConfig.RootCAs = cfg.RootCAs
	}
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		Dial: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).Dial,
		TLSClientConfig:     tlsConfig,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	if cfg.Proxy != nil {
		tr.Proxy = cfg.Proxy
	}

	c := &Client{
		Auth: &AuthConfig{
			PrivateKey:            pk,
			ClientName:            cfg.Name,
			AuthenticationVersion: cfg.AuthenticationVersion,
		},
		client: &http.Client{
			Transport: tr,
			Timeout:   time.Duration(cfg.Timeout) * time.Second,
		},
		BaseURL: baseUrl,
	}
	c.IsWebuiKey = cfg.IsWebuiKey
	c.ACLs = &ACLService{client: c}
	c.AuthenticateUser = &AuthenticateUserService{client: c}
	c.Associations = &AssociationService{client: c}
	c.Clients = &ApiClientService{client: c}
	c.Containers = &ContainerService{client: c}
	c.Cookbooks = &CookbookService{client: c}
	c.CookbookArtifacts = &CBAService{client: c}
	c.DataBags = &DataBagService{client: c}
	c.Environments = &EnvironmentService{client: c}
	c.Groups = &GroupService{client: c}
	c.License = &LicenseService{client: c}
	c.Nodes = &NodeService{client: c}
	c.Organizations = &OrganizationService{client: c}
	c.Policies = &PolicyService{client: c}
	c.PolicyGroups = &PolicyGroupService{client: c}
	c.RequiredRecipe = &RequiredRecipeService{client: c}
	c.Principals = &PrincipalService{client: c}
	c.Roles = &RoleService{client: c}
	c.Sandboxes = &SandboxService{client: c}
	c.Search = &SearchService{client: c}
	c.Stats = &StatsService{client: c}
	c.Status = &StatusService{client: c}
	c.UpdatedSince = &UpdatedSinceService{client: c}
	c.Universe = &UniverseService{client: c}
	c.Users = &UserService{client: c}
	return c, nil
}

func NewClientWithOutConfig(baseurl string) (*Client, error) {
	baseUrl, _ := url.Parse(baseurl)
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		Dial: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).Dial,
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		TLSHandshakeTimeout: 10 * time.Second,
	}

	c := &Client{
		client: &http.Client{
			Transport: tr,
			Timeout:   60 * time.Second,
		},
		BaseURL: baseUrl,
	}

	return c, nil
}
func (cfg *Config) VerifyVersion() (err error) {
	if cfg.AuthenticationVersion != "1.3" {
		cfg.AuthenticationVersion = "1.0"
	}
	return
}

// basicRequestDecoder performs a request on an endpoint, and decodes the response into the passed in Type
// basicRequestDecoder is the same code as magic RequestDecoder with the addition of a generated Authentication: Basic header
// to the http request
func (c *Client) basicRequestDecoder(method, path string, body io.Reader, v interface{}, user string, password string) error {
	req, err := c.NewRequest(method, path, body)
	if err != nil {
		return err
	}

	basicAuthHeader(req, user, password)

	debug("\n\nRequest: %+v \n", req)
	res, err := c.Do(req, v)
	if res != nil {
		defer res.Body.Close()
	}
	debug("Response: %+v\n", res)
	if err != nil {
		return err
	}
	return err
}

// magicRequestDecoder performs a request on an endpoint, and decodes the response into the passed in Type
func (c *Client) magicRequestDecoder(method, path string, body io.Reader, v interface{}) error {
	req, err := c.NewRequest(method, path, body)
	if err != nil {
		return err
	}

	debug("\n\nRequest: %+v \n", req)
	res, err := c.Do(req, v)
	if res != nil {
		defer res.Body.Close()
	}
	debug("Response: %+v\n", res)
	if err != nil {
		return err
	}
	return err
}

// NewRequest returns a signed request  suitable for the chef server
func (c *Client) NewRequest(method string, requestUrl string, body io.Reader) (*http.Request, error) {
	relativeUrl, err := url.Parse(requestUrl)
	if err != nil {
		return nil, err
	}
	u := c.BaseURL.ResolveReference(relativeUrl)

	// NewRequest uses a new value object of body
	req, err := http.NewRequest(method, u.String(), body)
	if err != nil {
		return nil, err
	}

	// parse and encode Querystring Values
	values := req.URL.Query()
	req.URL.RawQuery = values.Encode()
	debug("Encoded url %+v\n", u)

	myBody := &Body{body}

	if body != nil {
		// Detect Content-type
		req.Header.Set("Content-Type", myBody.ContentType())
	}

	// Calculate the body hash
	if c.Auth.AuthenticationVersion == "1.3" {
		req.Header.Set("X-Ops-Content-Hash", myBody.Hash256())
	} else {
		req.Header.Set("X-Ops-Content-Hash", myBody.Hash())
	}

	if c.IsWebuiKey {
		req.Header.Set("X-Ops-Request-Source", "web")
	}
	err = c.Auth.SignRequest(req)
	if err != nil {
		return nil, err
	}

	return req, nil
}

// NoAuthNewRequest returns a request  suitable for public apis
func (c *Client) NoAuthNewRequest(method string, requestUrl string, body io.Reader) (*http.Request, error) {
	relativeUrl, err := url.Parse(requestUrl)
	if err != nil {
		return nil, err
	}
	u := c.BaseURL.ResolveReference(relativeUrl)

	// NewRequest uses a new value object of body
	req, err := http.NewRequest(method, u.String(), body)
	if err != nil {
		return nil, err
	}

	// parse and encode Querystring Values
	values := req.URL.Query()
	req.URL.RawQuery = values.Encode()
	debug("Encoded url %+v\n", u)

	myBody := &Body{body}

	if body != nil {
		// Detect Content-type
		req.Header.Set("Content-Type", myBody.ContentType())
	}
	return req, nil
}

// basicAuth does base64 encoding of a user and password
func basicAuth(user string, password string) string {
	creds := user + ":" + password
	return base64.StdEncoding.EncodeToString([]byte(creds))
}

// basicAuthHeader adds an Authentication Basic header to the request
// The user and password values should be clear text. They will be
// base64 encoded for the header.
func basicAuthHeader(r *http.Request, user string, password string) {
	r.Header.Add("authorization", "Basic "+basicAuth(user, password))
}

// CheckResponse receives a pointer to a http.Response and generates an Error via unmarshalling
func CheckResponse(r *http.Response) error {
	if c := r.StatusCode; 200 <= c && c <= 299 {
		return nil
	}
	errorResponse := &ErrorResponse{Response: r}
	data, err := ioutil.ReadAll(r.Body)
	debug("Response Error Body: %+v\n", string(data))
	if err == nil && data != nil {
		json.Unmarshal(data, errorResponse)
		errorResponse.ErrorText = data
		errorResponse.ErrorMsg = extractErrorMsg(data)
	}
	return errorResponse
}

// extractErrorMsg makes a best faith effort to extract the error message text
// from the response body returned from the Chef Server. Error messages are
// typically formatted in a json body as {"error": ["msg"]}
func extractErrorMsg(data []byte) string {
	errorMsg := &ErrorMsg{}
	json.Unmarshal(data, errorMsg)
	switch t := errorMsg.Error.(type) {
	case []interface{}:
		// Return the string as a byte stream
		var rmsg string
		for _, val := range t {
			switch inval := val.(type) {
			case string:
				rmsg = rmsg + inval + "\n"
			default:
				debug("Unknown type  %+v data %+v\n", inval, val)
			}
			return strings.TrimSpace(rmsg)
		}
	default:
		debug("Unknown type  %+v data %+v msg %+v\n", t, string(data), errorMsg.Error)
	}
	return ""
}

//  ChefError tries to unwind a chef client err return embedded in an error
//  Unwinding allows easy access the StatusCode, StatusMethod and StatusURL functions
func ChefError(err error) (cerr *ErrorResponse, nerr error) {
	if err == nil {
		return cerr, err
	}
	if cerr, ok := err.(*ErrorResponse); ok {
		return cerr, err
	}
	return cerr, err
}

// Do is used either internally via our magic request shite or a user may use it
func (c *Client) Do(req *http.Request, v interface{}) (*http.Response, error) {
	res, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}

	// BUG(fujin) tightly coupled
	err = CheckResponse(res)
	if err != nil {
		return res, err
	}

	var resBuf bytes.Buffer
	resTee := io.TeeReader(res.Body, &resBuf)

	// add the body back to the response so
	// subsequent calls to res.Body contain data
	res.Body = ioutil.NopCloser(&resBuf)

	// no response interface specified
	if v == nil {
		if debug_on() {
			// show the response body as a string
			resbody, _ := ioutil.ReadAll(resTee)
			debug("Response body: %+v\n", string(resbody))
		} else {
			_, _ = ioutil.ReadAll(resTee)
		}
		debug("No response body requested\n")
		return res, nil
	}

	// response interface, v, is an io writer
	if w, ok := v.(io.Writer); ok {
		debug("Response output desired is an io Writer\n")
		_, err = io.Copy(w, resTee)
		return res, err
	}

	// response content-type specifies JSON encoded - decode it
	if hasJsonContentType(res) {
		err = json.NewDecoder(resTee).Decode(v)
		if debug_on() {
			// show the response body as a string
			resbody, _ := ioutil.ReadAll(&resBuf)
			debug("Response body: %+v\n", string(resbody))
			var repBuffer bytes.Buffer
			repBuffer.Write(resbody)
			res.Body = ioutil.NopCloser(&repBuffer)
		}
		debug("Response body specifies content as JSON: %+v Err: %+v\n", v, err)
		return res, err
	}

	// response interface, v, is type string and the content is plain text
	if _, ok := v.(*string); ok && hasTextContentType(res) {
		resbody, _ := ioutil.ReadAll(resTee)
		if err != nil {
			return res, err
		}
		out := string(resbody)
		debug("Response body parsed as string: %+v\n", out)
		*v.(*string) = out
		return res, nil
	}

	// Default response: Content-Type is not JSON. Assume v is a struct and decode the response as json
	err = json.NewDecoder(resTee).Decode(v)
	if debug_on() {
		// show the response body as a string
		resbody, _ := ioutil.ReadAll(&resBuf)
		debug("Response body: %+v\n", string(resbody))
		var repBuffer bytes.Buffer
		repBuffer.Write(resbody)
		res.Body = ioutil.NopCloser(&repBuffer)
	}
	debug("Response body defaulted to JSON parsing: %+v Err: %+v\n", v, err)
	return res, err
}

func hasJsonContentType(res *http.Response) bool {
	contentType := res.Header.Get("Content-Type")
	return contentType == "application/json"
}

func hasTextContentType(res *http.Response) bool {
	contentType := res.Header.Get("Content-Type")
	return contentType == "text/plain"
}

// SignRequest modifies headers of an http.Request
func (ac AuthConfig) SignRequest(request *http.Request) error {
	var request_headers []string
	var endpoint string
	if request.URL.Path != "" {
		endpoint = path.Clean(request.URL.Path)
		request.URL.Path = endpoint
	} else {
		endpoint = request.URL.Path
	}

	vals := map[string]string{
		"Method":                   request.Method,
		"Accept":                   "application/json",
		"X-Chef-Version":           ChefVersion,
		"X-Ops-Server-API-Version": "1",
		"X-Ops-Timestamp":          time.Now().UTC().Format(time.RFC3339),
		"X-Ops-Content-Hash":       request.Header.Get("X-Ops-Content-Hash"),
		"X-Ops-UserId":             ac.ClientName,
		"X-Ops-Request-Source":     request.Header.Get("X-Ops-Request-Source"),
	}

	if ac.AuthenticationVersion == "1.3" {
		vals["Path"] = endpoint
		vals["X-Ops-Sign"] = "version=1.3"
		request_headers = []string{"Method", "Path", "Accept", "X-Chef-Version", "X-Ops-Server-API-Version", "X-Ops-Timestamp", "X-Ops-UserId", "X-Ops-Sign", "X-Ops-Request-Source"}
	} else {
		vals["Hashed Path"] = HashStr(endpoint)
		vals["X-Ops-Sign"] = "algorithm=sha1;version=1.0"
		request_headers = []string{"Method", "Accept", "X-Chef-Version", "X-Ops-Server-API-Version", "X-Ops-Timestamp", "X-Ops-UserId", "X-Ops-Sign", "X-Ops-Request-Source"}
	}

	// Add the vals to the request
	for _, key := range request_headers {
		request.Header.Set(key, vals[key])
	}

	content := ac.SignatureContent(vals)

	// generate signed string of headers
	var signature []byte
	var err error
	if ac.AuthenticationVersion == "1.3" {
		signature, err = GenerateDigestSignature(ac.PrivateKey, content)
		if err != nil {
			fmt.Printf("Error from signature %+v\n", err)
			return err
		}
	} else {
		signature, err = GenerateSignature(ac.PrivateKey, content)
		if err != nil {
			return err
		}
	}

	// THIS IS CHEF PROTOCOL SPECIFIC
	// Signature is made up of n 60 length chunks
	base64sig := Base64BlockEncode(signature, 60)

	// roll over the auth slice and add the apropriate header
	for index, value := range base64sig {
		request.Header.Set(fmt.Sprintf("X-Ops-Authorization-%d", index+1), string(value))
	}

	return nil
}

func (ac AuthConfig) SignatureContent(vals map[string]string) (content string) {
	// sanitize the path for the chef-server
	// chef-server doesn't support '//' in the Hash Path.

	// The signature is very particular, the exact headers and the order they are included in the signature matter
	var signed_headers []string

	if ac.AuthenticationVersion == "1.3" {
		signed_headers = []string{"Method", "Path", "X-Ops-Content-Hash", "X-Ops-Sign", "X-Ops-Timestamp",
			"X-Ops-UserId", "X-Ops-Server-API-Version"}
	} else {
		signed_headers = []string{"Method", "Hashed Path", "X-Ops-Content-Hash", "X-Ops-Timestamp", "X-Ops-UserId"}
	}

	for _, key := range signed_headers {
		content += fmt.Sprintf("%s:%s\n", key, vals[key])
	}

	content = strings.TrimSuffix(content, "\n")
	return
}

// PrivateKeyFromString parses an private key from a string
func PrivateKeyFromString(key []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(key)
	if block == nil {
		return nil, fmt.Errorf("private key block size invalid")
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		switch key := key.(type) {
		case *rsa.PrivateKey:
			return key, nil
		default:
			return nil, errors.New("tls: found unknown private key type in PKCS#8 wrapping")
		}
	}

	return nil, errors.New("tls: failed to parse private key")
}

func (c *Client) MagicRequestResponseDecoderWithOutAuth(url, method string, body io.Reader, v interface{}) error {
	req, err := c.NoAuthNewRequest(method, url, body)
	if err != nil {
		return err
	}

	res, err := c.Do(req, v)
	if res != nil {
		defer res.Body.Close()
	}
	if err != nil {
		return err
	}
	return err
}
