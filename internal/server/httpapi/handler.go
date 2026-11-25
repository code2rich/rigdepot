// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
)

// The problem codes this layer produces itself, before a request reaches the
// domain or when the domain returns an error instead of a response. The
// contract lists them under "Common errors".
const (
	CodeRequestInvalid       = "request.invalid"
	CodePreconditionRequired = "precondition.required"
	CodeServerError          = "server.error"
)

// NewHandler serves the whole contract on mux: impl answers the operations,
// and everything that goes wrong on the way becomes a problem document. A
// parameter that does not bind is 400 request.invalid naming the parameter, a
// missing If-Match is 428 precondition.required, a body that does not decode
// is 400 request.invalid, and an error impl returns instead of a response is
// 500 server.error.
//
// Authentication, the request id, the language and cross-origin protection are
// middleware around this handler and are added when the server is assembled.
func NewHandler(impl StrictServerInterface, mux *http.ServeMux) http.Handler {
	strict := NewStrictHandlerWithOptions(impl, nil, StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			WriteProblem(w, Problem{Status: http.StatusBadRequest, Code: CodeRequestInvalid, Detail: ptr("The request body is not valid JSON for this operation.")})
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			WriteProblem(w, Problem{Status: http.StatusInternalServerError, Code: CodeServerError})
		},
	})
	return HandlerWithOptions(strict, StdHTTPServerOptions{
		BaseRouter:       mux,
		ErrorHandlerFunc: parameterError,
	})
}

// parameterError turns a parameter that could not be bound into a problem
// document.
func parameterError(w http.ResponseWriter, r *http.Request, err error) {
	var missingHeader *RequiredHeaderError
	if errors.As(err, &missingHeader) && missingHeader.ParamName == "If-Match" {
		WriteProblem(w, Problem{Status: http.StatusPreconditionRequired, Code: CodePreconditionRequired,
			Detail: ptr("Send the ETag this change is based on in If-Match.")})
		return
	}
	field, code := "", "invalid"
	var (
		missingQuery *RequiredParamError
		badFormat    *InvalidParamFormatError
		tooMany      *TooManyValuesForParamError
		badJSON      *UnmarshalingParamError
		badCookie    *UnescapedCookieParamError
	)
	switch {
	case errors.As(err, &missingHeader):
		field, code = missingHeader.ParamName, "required"
	case errors.As(err, &missingQuery):
		field, code = missingQuery.ParamName, "required"
	case errors.As(err, &badFormat):
		field = badFormat.ParamName
	case errors.As(err, &tooMany):
		field = tooMany.ParamName
	case errors.As(err, &badJSON):
		field = badJSON.ParamName
	case errors.As(err, &badCookie):
		field = badCookie.ParamName
	}
	p := Problem{Status: http.StatusBadRequest, Code: CodeRequestInvalid, Detail: ptr("The request has invalid parameters.")}
	if field != "" {
		p.Errors = &[]FieldError{{Field: field, Code: code}}
	}
	WriteProblem(w, p)
}

func ptr[T any](v T) *T { return &v }

// WriteProblem writes p as application/problem+json. Type and Title are
// filled in from the status when they are empty.
func WriteProblem(w http.ResponseWriter, p Problem) {
	if p.Type == "" {
		p.Type = "about:blank"
	}
	if p.Title == "" {
		p.Title = http.StatusText(p.Status)
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}
