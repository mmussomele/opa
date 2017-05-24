// Copyright 2016 The OPA Authors.  All rights reserved.
// Use of this source code is governed by an Apache2
// license that can be found in the LICENSE file.

package server

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/open-policy-agent/opa/server/types"
	"github.com/open-policy-agent/opa/server/writer"
)

func httpV0DataPost(s *Server) func(w http.ResponseWriter, r *http.Request) {
	return httpWrapper(s.v0DataPost)
}

func httpV1DataPut(s *Server) func(w http.ResponseWriter, r *http.Request) {
	return httpWrapper(s.v1DataPut)
}

func httpV1DataGet(s *Server) func(w http.ResponseWriter, r *http.Request) {
	return httpWrapper(s.v1DataGet)
}

func httpV1DataPatch(s *Server) func(w http.ResponseWriter, r *http.Request) {
	return httpWrapper(s.v1DataPatch)
}

func httpV1DataPost(s *Server) func(w http.ResponseWriter, r *http.Request) {
	return httpWrapper(s.v1DataPost)
}

func httpV1QueryGet(s *Server) func(w http.ResponseWriter, r *http.Request) {
	return httpWrapper(s.v1QueryGet)
}

func httpV1PoliciesPut(s *Server) func(w http.ResponseWriter, r *http.Request) {
	return httpWrapper(s.v1PoliciesPut)
}

func httpV1PoliciesList(s *Server) func(w http.ResponseWriter, r *http.Request) {
	return httpWrapper(s.v1PoliciesList)
}

func httpV1PoliciesGet(s *Server) func(w http.ResponseWriter, r *http.Request) {
	return httpWrapper(s.v1PoliciesGet)
}

func httpV1PoliciesDelete(s *Server) func(w http.ResponseWriter, r *http.Request) {
	return httpWrapper(s.v1PoliciesDelete)
}

func httpWrapper(fn func(request) response) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		req := httpReqToServerReq(r)
		resp := fn(req)
		serverRespToHttpResp(w, resp)
	}
}

func httpReqToServerReq(r *http.Request) request {
	values := r.URL.Query()
	return request{
		ctx:            r.Context(),
		values:         values,
		path:           mux.Vars(r)["path"],
		input:          r.Body,
		getSource:      getBoolParam(r.URL, types.ParamSourceV1, true),
		noneMatch:      r.Header.Get("If-None-Match"),
		pretty:         getBoolParam(r.URL, types.ParamPrettyV1, true),
		explainMode:    getExplain(values["explain"]),
		includeMetrics: getBoolParam(r.URL, types.ParamMetricsV1, true),
	}
}

// Since the defined values of r match to the parameters of exactly one
// writer.* function, we can figure out what kind of response to write based
// on the field values.
func serverRespToHttpResp(w http.ResponseWriter, r response) {
	r.code = toHttpCode[r.code]
	if r.err != nil {
		if r.msg == "" {
			writer.ErrorAuto(w, r.err)
		} else {
			writer.ErrorString(w, r.code, r.msg, r.err)
		}
		return
	}

	// Nil has no type, so we need to check for it.
	if r.data == nil {
		writer.Bytes(w, r.code, nil)
		return
	}

	switch data := r.data.(type) {
	case []byte:
		writer.Bytes(w, r.code, data)
	default:
		writer.JSON(w, r.code, data, r.pretty)
	}
}

var toHttpCode = map[int]int{
	CodeOK:          http.StatusOK,
	CodeNotFound:    http.StatusNotFound,
	CodeBadRequest:  http.StatusBadRequest,
	CodeNoContent:   http.StatusNoContent,
	CodeNotModified: http.StatusNotModified,
}
