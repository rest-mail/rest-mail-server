// Package endpoint registers each API route with the Go types it takes and answers (#623).
//
// Registration does the work as well as describing it. It binds the request into the
// request type, runs the handler, and writes what the handler returns in respond's envelope,
// so the types the OpenAPI document describes are the types the server executes. The same
// registration is the operation's entry in the generated document (go-oapifly), so a route
// cannot be served without being described.
//
// A request type names its parameters with `path:"name"`, `query:"name"` and `header:"name"`
// tags, and its body with a `body:""` field; a struct with no parameter fields is the body
// itself. Its constraints are struct tags (minLength, maximum, pattern, enums, format and so
// on), described in the document and enforced by go-oapifly's Bind before the handler runs:
// a request that breaks them is answered 422 with the fields at fault. The handler returns
// the response value, or an *Error to refuse the request.
package endpoint

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	oapifly "github.com/antimatter-studios/go-oapifly"
	"github.com/go-chi/chi/v5"

	"github.com/restmail/restmail/internal/api/respond"
)

// Operation is how a route is registered: where it is, what it is called, and what it can
// answer besides its success.
type Operation struct {
	Method      string
	Path        string
	OperationID string
	Summary     string
	Description string
	Tags        []string

	// Status is the success status. 204 writes no body.
	Status int
	// Errors are the error statuses the operation can answer. Each is described with
	// respond's error envelope. 422 for a request that breaks its type's rules is added when
	// binding can reject a parameter or body, 400 for a body that is not JSON and 413 for one
	// over the size cap when it has a body, and 500 always.
	Errors []int

	// Responses overrides individual response bodies when the answer belongs to an upstream
	// service (for example, a sidecar error beside the API's own binding errors).
	// Several entries for one status describe its distinct media types.
	Responses []oapifly.EndpointResponse

	// SuccessStatuses are additional success statuses carrying the same response type.
	SuccessStatuses []int

	// Headers are the response headers every success answer carries, by name, for a handler
	// that answers Headed: Set-Cookie on a sign-in.
	Headers map[string]oapifly.ResponseHeader

	// MediaType is the success answer's media type when the handler answers a Document, Served
	// or a Stream: a page, a YAML document, a metrics exposition, a download or an event
	// stream rather than the JSON envelope.
	MediaType string

	// Security is the operation's own requirement, when it differs from what its middleware
	// enforces: alternatives of schemes, each with the roles held there. nil leaves it to the
	// middleware and the document's default; oapifly.Public needs no credential.
	Security []oapifly.SecurityRequirement

	// Middleware runs before binding, in order, after the middleware its Routes already
	// apply: a capability check, a rate limit. Each describes what it adds.
	Middleware []Middleware
}

// Middleware is a middleware and a description of what it adds to every operation it wraps:
// the responses it can answer, the security it enforces and the roles it requires. The two
// are kept together so a route cannot be guarded by a check the document does not state.
type Middleware struct {
	Wrap      func(http.Handler) http.Handler
	Describes oapifly.Middleware
}

// Routes is where operations are registered: a router, the registry recording them, and what
// the middleware the router already applies to every route (its Use) adds, which chi runs but
// cannot describe.
type Routes struct {
	Router       chi.Router
	Registry     *Registry
	descriptions []routeDescription

	// ResponseHeaders describes headers added by the router middleware to every status.
	ResponseHeaders func(path string) map[string]oapifly.ResponseHeader
}

// Describing returns rs with the descriptions of middleware applied to rs.Router since, for
// a group that adds its own Use.
func (rs Routes) Describing(applied ...oapifly.Middleware) Routes {
	return rs.DescribingWhen(nil, applied...)
}

// DescribingWhen records middleware only for methods it actually protects. The
// predicate and descriptions are evaluated at registration, without changing
// the router's executable middleware. nil describes every method.
func (rs Routes) DescribingWhen(applies func(method string) bool, applied ...oapifly.Middleware) Routes {
	rs.descriptions = append(append([]routeDescription(nil), rs.descriptions...), routeDescription{
		applies: applies, middleware: append([]oapifly.Middleware(nil), applied...),
	})
	return rs
}

type routeDescription struct {
	applies    func(string) bool
	middleware []oapifly.Middleware
}

func (rs Routes) appliedTo(method string) []oapifly.Middleware {
	var applied []oapifly.Middleware
	for _, description := range rs.descriptions {
		if description.applies == nil || description.applies(strings.ToUpper(method)) {
			applied = append(applied, description.middleware...)
		}
	}
	return applied
}

// Error is a handler's refusal: the status, the API's error code and message, and, for a
// validation failure, the fields at fault.
type Error struct {
	Status  int
	Code    string
	Message string
	Fields  map[string]string
	// Header is written with the refusal: the Retry-After of a 429.
	Header http.Header
	// text writes Message as plain text, as http.Error writes it (RefuseText).
	text bool
}

func (e *Error) Error() string { return fmt.Sprintf("%d %s: %s", e.Status, e.Code, e.Message) }

// RefuseText is a refusal written as plain text, the message and a newline, as http.Error
// writes it: for the documents a mail client fetches (autoconfig, autodiscover), which have
// always answered so and whose clients read no JSON. The operation describes it in
// Operation.Responses, as text/plain.
func RefuseText(status int, message string) *Error {
	return &Error{Status: status, Message: message, text: true}
}

// Refuse is an *Error with a status, code and message.
func Refuse(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// The messages of a 422, by the part of the request its fields are in (#786).
const (
	pathFailedValidation    = "Path parameters failed validation"
	queryFailedValidation   = "Query parameters failed validation"
	headerFailedValidation  = "Request headers failed validation"
	requestFailedValidation = "Request failed validation"
)

// Invalid is the 422 validation failure, field by field, for fields of the request body.
func Invalid(fields map[string]string) *Error {
	return &Error{Status: http.StatusUnprocessableEntity, Code: "validation_failed",
		Message: respond.BodyFailedValidation, Fields: fields}
}

// InvalidQuery is the 422 validation failure for query parameters a handler refuses itself.
func InvalidQuery(fields map[string]string) *Error {
	refusal := Invalid(fields)
	refusal.Message = queryFailedValidation
	return refusal
}

// Data is the {"data": ...} envelope respond.Data writes, as a type the document can name.
type Data[T any] struct {
	Data T `json:"data"`
}

// List is a collection answer: written as respond.List writes it, {"data": [...],
// "pagination": {...}}, and described as such.
type List[T any] struct {
	Data       []T                 `json:"data"`
	Pagination *respond.Pagination `json:"pagination,omitempty"`
}

// listResponse is how write recognises a List, whatever its element type.
type listResponse interface{ listEnvelope() }

func (List[T]) listEnvelope() {}

// Status carries a success value whose status depends on the handler's result.
// Each possible status is declared in Operation.Status or SuccessStatuses.
type Status[T any] struct {
	Code  int
	Value T
}

func (s Status[T]) responseStatus() (int, interface{}) { return s.Code, s.Value }
func (Status[T]) responseType() reflect.Type           { return reflect.TypeOf((*T)(nil)).Elem() }
func (Status[T]) responseEnvelope() reflect.Type       { return reflect.TypeOf((*Data[T])(nil)).Elem() }

type statusResponse interface {
	responseStatus() (int, interface{})
	responseType() reflect.Type
	responseEnvelope() reflect.Type
}

// Empty is the response type of an operation that answers with no body.
type Empty struct{}

// Registry collects the registered operations for the generated document.
type Registry struct {
	endpoints []oapifly.Endpoint
	routes    map[string]bool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{routes: map[string]bool{}}
}

// Endpoints returns the registered operations, in the order they were registered.
func (reg *Registry) Endpoints() []oapifly.Endpoint {
	return append([]oapifly.Endpoint(nil), reg.endpoints...)
}

// Routes returns every registered route as "METHOD pattern", with the pattern as it was
// mounted on the router, sorted.
func (reg *Registry) Routes() []string {
	out := make([]string, 0, len(reg.routes))
	for route := range reg.routes {
		out = append(out, route)
	}
	sort.Strings(out)
	return out
}

// Has reports whether method and path were registered, so a check can find a route that
// was mounted without a registration.
func (reg *Registry) Has(method, path string) bool {
	return reg.routes[strings.ToUpper(method)+" "+path]
}

// Register mounts handler on rs.Router at op.Method and op.Path, behind op.Middleware, and
// records the operation in rs.Registry with everything its middleware adds. Req is bound from
// the request and Resp is what the handler answers.
func Register[Req, Resp any](rs Routes, op Operation, handler func(*http.Request, Req) (Resp, error)) {
	if op.Status == 0 {
		op.Status = http.StatusOK
	}
	respType := reflect.TypeOf((*Resp)(nil)).Elem()
	if documents := respType == reflect.TypeOf(Document{}) || respType == reflect.TypeOf(Served{}) || respType == reflect.TypeOf(Stream{}); documents != (op.MediaType != "") {
		panic("endpoint: " + op.Method + " " + op.Path + " needs a MediaType exactly when it answers a Document, Served or a Stream")
	}
	reqType := reflect.TypeOf((*Req)(nil)).Elem()
	shape := requestShape(reqType)

	rs.Registry.record(rs.appliedTo(op.Method), op, reqType, respType, reflect.TypeOf((*Data[Resp])(nil)).Elem(), shape)
	if rs.ResponseHeaders != nil {
		rs.Registry.addResponseHeaders(rs.ResponseHeaders(op.Path))
	}

	wraps := make([]func(http.Handler) http.Handler, len(op.Middleware))
	for i, m := range op.Middleware {
		wraps[i] = m.Wrap
	}
	rs.Router.With(wraps...).Method(op.Method, op.Path, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var in Req
		if err := bind(req, &in, shape); err != nil {
			writeError(w, err)
			return
		}
		out, err := handler(req, in)
		if err != nil {
			writeError(w, err)
			return
		}
		answer := any(out)
		if served, ok := answer.(Served); ok {
			served.serve(w, req)
			return
		}
		if stream, ok := answer.(Stream); ok {
			stream.stream(w, op.Status)
			return
		}
		if headed, ok := answer.(headedResponse); ok {
			_, answer = headed.responseHeader()
		}
		if reply, ok := answer.(statusResponse); ok {
			status, _ := reply.responseStatus()
			declared := status == op.Status
			for _, additional := range op.SuccessStatuses {
				declared = declared || status == additional
			}
			if !declared {
				writeError(w, fmt.Errorf("endpoint: undeclared success status %d", status))
				return
			}
		}
		write(w, op.Status, out)
	}))
}

func (reg *Registry) record(applied []oapifly.Middleware, op Operation, reqType, respType, envelope reflect.Type, shape shape) {
	key := strings.ToUpper(op.Method) + " " + op.Path
	if reg.routes[key] {
		panic("endpoint: " + key + " registered twice")
	}
	reg.routes[key] = true

	if respType.Implements(reflect.TypeOf((*headedResponse)(nil)).Elem()) {
		headed := reflect.Zero(respType).Interface().(headedResponse)
		respType, envelope = headed.headedType(), headed.headedEnvelope()
	}
	if respType.Implements(reflect.TypeOf((*statusResponse)(nil)).Elem()) {
		reply := reflect.Zero(respType).Interface().(statusResponse)
		respType, envelope = reply.responseType(), reply.responseEnvelope()
	}
	responses := []oapifly.EndpointResponse{successResponse(op, op.Status, respType, envelope)}
	for _, status := range op.SuccessStatuses {
		responses = append(responses, successResponse(op, status, respType, envelope))
	}
	if op.Headers != nil {
		for i := range responses {
			responses[i].Headers = op.Headers
		}
	}
	errorStatuses := map[int]bool{http.StatusInternalServerError: true}
	if shape.validates {
		errorStatuses[http.StatusUnprocessableEntity] = true
	}
	if shape.body {
		errorStatuses[http.StatusBadRequest] = true
		errorStatuses[http.StatusRequestEntityTooLarge] = true
	}
	if shape.forms > 1 {
		errorStatuses[http.StatusUnsupportedMediaType] = true
	}
	for _, status := range op.Errors {
		errorStatuses[status] = true
	}
	statuses := make([]int, 0, len(errorStatuses))
	for status := range errorStatuses {
		statuses = append(statuses, status)
	}
	sort.Ints(statuses)
	for _, status := range statuses {
		body := reflect.TypeOf(respond.ErrorResponse{})
		if status == http.StatusUnprocessableEntity {
			body = reflect.TypeOf(respond.ValidationErrorResponse{})
		}
		response := oapifly.EndpointResponse{Status: status, Type: body}
		if status == http.StatusTooManyRequests {
			response.Headers = map[string]oapifly.ResponseHeader{
				"Retry-After": {Description: "Seconds until another request is allowed", Required: true, Type: oapifly.TypeOf[int]()},
			}
		}
		responses = append(responses, response)
	}

	// Replace the automatic description once, retaining its position, then keep
	// every additional media type declared for that status.
	overridden := map[int]bool{}
	for _, override := range op.Responses {
		replaced := false
		if !overridden[override.Status] {
			for i := range responses {
				if responses[i].Status == override.Status {
					responses[i] = override
					replaced = true
					break
				}
			}
			overridden[override.Status] = true
		}
		if !replaced {
			responses = append(responses, override)
		}
	}

	var request reflect.Type
	if shape.binds {
		request = reqType
	}
	reg.endpoints = append(reg.endpoints, oapifly.Endpoint{
		Method:      op.Method,
		Path:        documentedPath(op.Path),
		OperationID: op.OperationID,
		Summary:     op.Summary,
		Description: op.Description,
		Tags:        op.Tags,
		Request:     request,
		Responses:   responses,
		Security:    op.Security,
		Middleware:  described(applied, op.Middleware),
	})
}

// addResponseHeaders decorates the operation just registered, including refusals
// from its middleware. Copy shared declarations so another listener is unchanged.
func (reg *Registry) addResponseHeaders(headers map[string]oapifly.ResponseHeader) {
	if len(headers) == 0 {
		return
	}
	e := &reg.endpoints[len(reg.endpoints)-1]
	decorate := func(responses []oapifly.EndpointResponse) []oapifly.EndpointResponse {
		out := append([]oapifly.EndpointResponse(nil), responses...)
		for i := range out {
			combined := make(map[string]oapifly.ResponseHeader, len(out[i].Headers)+len(headers))
			for name, h := range headers {
				combined[name] = h
			}
			for name, h := range out[i].Headers {
				combined[name] = h
			}
			out[i].Headers = combined
		}
		return out
	}
	e.Responses = decorate(e.Responses)
	e.Middleware = append([]oapifly.Middleware(nil), e.Middleware...)
	for i := range e.Middleware {
		e.Middleware[i].Responses = decorate(e.Middleware[i].Responses)
	}
}

// described lists what every middleware wrapping an operation adds, outermost first.
func described(applied []oapifly.Middleware, own []Middleware) []oapifly.Middleware {
	out := append([]oapifly.Middleware(nil), applied...)
	for _, m := range own {
		out = append(out, m.Describes)
	}
	return out
}

// successResponse describes the success answer in the envelope write puts it in: a List as
// itself, a Document or a Stream as text in its media type, Served as bytes in its media type, anything
// else as Data[Resp], the {"data": ...} body respond.Data writes.
func successResponse(op Operation, status int, respType, envelope reflect.Type) oapifly.EndpointResponse {
	if respType == reflect.TypeOf(Document{}) || respType == reflect.TypeOf(Stream{}) {
		return oapifly.EndpointResponse{Status: status, Type: reflect.TypeOf(""), MediaType: op.MediaType}
	}
	if respType == reflect.TypeOf(Served{}) {
		// Stored bytes, not text: go-oapifly describes a []byte in a media type that is not
		// JSON as that media type with no schema, OpenAPI 3.1's way of saying the bytes are
		// the body.
		return oapifly.EndpointResponse{Status: status, Type: reflect.TypeOf([]byte(nil)), MediaType: op.MediaType}
	}
	if raw, ok := reflect.Zero(respType).Interface().(forwardedResponse); ok {
		return oapifly.EndpointResponse{Status: status, Type: raw.responseType()}
	}
	if status == http.StatusNoContent || respType == reflect.TypeOf(Empty{}) {
		return oapifly.EndpointResponse{Status: status}
	}
	if respType.Implements(reflect.TypeOf((*listResponse)(nil)).Elem()) {
		return oapifly.EndpointResponse{Status: status, Type: respType}
	}
	return oapifly.EndpointResponse{Status: status, Type: envelope}
}

var routePattern = regexp.MustCompile(`\{([^}:]+):[^}]*\}`)

// documentedPath turns a chi pattern into an OpenAPI path template: {id:[0-9]+} is {id}.
func documentedPath(path string) string {
	return routePattern.ReplaceAllString(path, "{$1}")
}

func write(w http.ResponseWriter, status int, out interface{}) {
	if doc, ok := out.(Document); ok {
		doc.write(w, status)
		return
	}
	if raw, ok := out.(forwardedResponse); ok {
		raw.writeResponse(w)
		return
	}
	out = unheaded(w, out)
	if reply, ok := out.(statusResponse); ok {
		status, out = reply.responseStatus()
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	if _, empty := out.(Empty); empty {
		w.WriteHeader(status)
		return
	}
	// The document says a list is never null, so a list the handler left nil is written as
	// [] and a map as {}.
	out = oapifly.EmptyLists(out)
	if _, isList := out.(listResponse); isList {
		respond.JSON(w, status, out)
		return
	}
	respond.Data(w, status, out)
}

func writeError(w http.ResponseWriter, err error) {
	var refusal *Error
	if !errors.As(err, &refusal) {
		slog.Error("api: handler failed", "error", err)
		respond.Error(w, http.StatusInternalServerError, "internal_error", "Internal server error")
		return
	}
	for name, values := range refusal.Header {
		for _, v := range values {
			w.Header().Add(name, v)
		}
	}
	if refusal.text {
		http.Error(w, refusal.Message, refusal.Status)
		return
	}
	if refusal.Fields != nil {
		message := refusal.Message
		if message == "" {
			message = respond.BodyFailedValidation
		}
		respond.ValidationFailed(w, message, refusal.Fields)
		return
	}
	respond.Error(w, refusal.Status, refusal.Code, refusal.Message)
}

// WriteError writes err as a registered route would answer it: the refusal it is, with its
// headers, or 500 for anything else. It is for a handler not yet registered that shares
// code with registered ones.
func WriteError(w http.ResponseWriter, err error) { writeError(w, err) }

// shape is what a request type takes from a request, read once at registration by the rules
// go-oapifly's Endpoint.Request describes it with.
type shape struct {
	binds     bool // anything at all: a parameter or a body
	validates bool // binding can refuse a parameter or body
	body      bool // a body
	forms     int  // the body fields: more than one is a body taken in several media types
}

func requestShape(t reflect.Type) shape {
	if t.Kind() != reflect.Struct {
		return shape{binds: true, validates: true, body: true}
	}
	params, forms, validates := false, 0, false
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		for _, in := range []string{"path", "query", "header"} {
			if f.Tag.Get(in) != "" {
				params = true
				validates = validates || parameterCanReject(f, in)
			}
		}
		if _, ok := f.Tag.Lookup("body"); ok {
			forms++
			validates = validates || bodyCanReject(f.Tag.Get("body"))
		}
	}
	bodyField := forms > 0
	whole := !params && !bodyField && t.NumField() > 0
	return shape{binds: params || bodyField || whole, validates: validates || whole, body: bodyField || whole, forms: forms}
}

// Raw body forms are copied without validation by go-oapifly. Only JSON
// forms can add body violations; parameter and explicit handler rules still apply.
func bodyCanReject(tag string) bool {
	if strings.Trim(tag, " ,") == "" {
		return true // An unnamed body form is JSON.
	}
	for _, media := range strings.Split(tag, ",") {
		base := strings.TrimSpace(strings.SplitN(media, ";", 2)[0])
		if base == "application/json" || strings.HasSuffix(base, "+json") {
			return true
		}
	}
	return false
}

// Plain optional query/header strings accept any text. A matched path segment
// is already present and nonempty, so a plain string needs a stronger rule to
// reject it. Named strings remain fallible because they may parse their text.
func parameterCanReject(field reflect.StructField, in string) bool {
	if field.Type != reflect.TypeOf("") {
		return true
	}
	if in == "path" {
		for _, rule := range []string{"enums", "maxLength", "pattern", "format"} {
			if _, present := field.Tag.Lookup(rule); present {
				return true
			}
		}
		minimum, err := strconv.Atoi(field.Tag.Get("minLength"))
		return err == nil && minimum > 1
	}
	if in != "query" && in != "header" {
		return true
	}
	for _, option := range strings.Split(field.Tag.Get(in), ",")[1:] {
		if option == "required" {
			return true
		}
	}
	for _, rule := range []string{"enums", "minLength", "maxLength", "pattern", "format"} {
		if _, present := field.Tag.Lookup(rule); present {
			return true
		}
	}
	return false
}

// bind reads the request into dst with go-oapifly, which enforces the rules the document
// describes: a violation is the 422 validation failure, field by field. A body in a media type
// none of the request type's forms takes is the 415. The body read is left for the handler to
// read again, for one that keeps a request as it came.
func bind(r *http.Request, dst interface{}, s shape) error {
	if !s.binds {
		return nil
	}
	var body []byte
	if s.body && r.Body != nil {
		read, err := io.ReadAll(r.Body)
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				return Refuse(http.StatusRequestEntityTooLarge, "request_too_large", "Request body too large")
			}
			return Refuse(http.StatusBadRequest, "bad_request", "Invalid request body")
		}
		body = read
		r.Body = io.NopCloser(bytes.NewReader(body))
	}

	textual := stringParams(reflect.TypeOf(dst))
	err := oapifly.Bind(dst, func(in, name string) (string, bool) {
		switch in {
		case "path":
			return pathParam(r, name)
		case "header":
			// net/http stores HTTP/1 Host and HTTP/2 :authority here,
			// separately from the ordinary header map.
			if strings.EqualFold(name, "Host") {
				return r.Host, r.Host != ""
			}
		case "query":
			values, ok := r.URL.Query()[name]
			if !ok || len(values) == 0 {
				return "", false
			}
			// A blank number, flag or time is an omitted one, as the handlers read
			// it with url.Values.Get: ?domain_id= lists every domain's entries. A
			// blank string is a value, which its enum may refuse: ?status= is not
			// a status.
			return values[0], values[0] != "" || textual[name]
		}
		v := r.Header.Get(name)
		return v, v != ""
	}, body)
	return bindError(dst, err)
}

// BindBody reads body into dst, a pointer to a type that is a body itself, as a registered
// operation binds its request: by the same rules, with the same refusals. It is for a body a
// handler has to unwrap first, such as a compressed one.
func BindBody(dst interface{}, body []byte) error {
	return bindError(dst, oapifly.Bind(dst, func(string, string) (string, bool) { return "", false }, body))
}

// bindError is the refusal a Bind error is: the 422 with the fields for violations, 400 for a
// body that cannot be read, 415 for one in a media type the request does not take.
func bindError(dst interface{}, err error) error {
	var violations oapifly.Violations
	switch {
	case err == nil:
		return nil
	case errors.As(err, &violations):
		missingMessages(reflect.TypeOf(dst).Elem(), "", violations)
		refusal := Invalid(violations)
		refusal.Message = refusedIn(reflect.TypeOf(dst).Elem(), violations)
		return refusal
	case errors.Is(err, oapifly.ErrMalformedBody):
		return Refuse(http.StatusBadRequest, "bad_request", "Invalid request body")
	case errors.Is(err, oapifly.ErrUnsupportedMediaType):
		return Refuse(http.StatusUnsupportedMediaType, "unsupported_media_type",
			"Unsupported media type: "+strings.TrimPrefix(err.Error(), oapifly.ErrUnsupportedMediaType.Error()+": "))
	}
	return err
}

// refusedIn is the message of a 422 for violations of a request of type t: the part of the
// request their fields are in, requestFailedValidation when they are in more than one.
// Bind names a parameter's violation by the parameter's name and a body's by its JSON path
// (#786).
func refusedIn(t reflect.Type, violations oapifly.Violations) string {
	params := paramsIn(t)
	where := ""
	for name := range violations {
		in := params[name]
		if in == "" {
			in = "body"
		}
		if where != "" && where != in {
			return requestFailedValidation
		}
		where = in
	}
	switch where {
	case "path":
		return pathFailedValidation
	case "query":
		return queryFailedValidation
	case "header":
		return headerFailedValidation
	}
	return respond.BodyFailedValidation
}

// paramsIn maps each parameter of t, a request type, to where it is ("path", "query" or
// "header"), read as Bind reads them: from t's own fields.
func paramsIn(t reflect.Type) map[string]string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	out := map[string]string{}
	if t.Kind() != reflect.Struct {
		return out
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		for _, in := range []string{"path", "query", "header"} {
			if tag := f.Tag.Get(in); tag != "" {
				out[strings.Split(tag, ",")[0]] = in
				break
			}
		}
	}
	return out
}

// stringParams names the query parameters of t whose type is a string.
func stringParams(t reflect.Type) map[string]bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	out := map[string]bool{}
	if t.Kind() != reflect.Struct {
		return out
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			for name := range stringParams(f.Type) {
				out[name] = true
			}
			continue
		}
		name := strings.Split(f.Tag.Get("query"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		kind := f.Type
		for kind.Kind() == reflect.Pointer {
			kind = kind.Elem()
		}
		if kind.Kind() == reflect.String {
			out[name] = true
		}
	}
	return out
}

// missingMessages gives a missing field the wording its missing tag documents.
// Bind reports a missing field as "required", whatever its message tag says: the
// message tag words a broken rule, and a field that is not there broke none.
func missingMessages(t reflect.Type, prefix string, violations oapifly.Violations) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		if _, body := f.Tag.Lookup("body"); body {
			missingMessages(f.Type, prefix, violations)
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		for _, in := range []string{"path", "query", "header"} {
			if param := f.Tag.Get(in); param != "" {
				name = strings.Split(param, ",")[0]
				break
			}
		}
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		key := prefix + name
		if message := f.Tag.Get("missing"); message != "" && violations[key] == "required" {
			violations[key] = message
		}
		// Recurse only for a reported nested field, avoiding recursive types.
		for field := range violations {
			if strings.HasPrefix(field, key+".") {
				missingMessages(f.Type, key+".", violations)
				break
			}
		}
	}
}

// pathParam is the named path parameter, decoded. chi routes on the escaped path when the
// request has one (r.URL.RawPath, set for an escaped slash): its parameters are then still
// escaped, and Work%2FProjects is decoded here to the folder Work/Projects. Otherwise they
// are decoded already, and decoding them again would make a%25b, the folder a%b, into a
// different name.
func pathParam(r *http.Request, name string) (string, bool) {
	v := chi.URLParam(r, name)
	if r.URL.RawPath != "" {
		decoded, err := url.PathUnescape(v)
		if err != nil {
			return "", false
		}
		v = decoded
	}
	return v, v != ""
}
