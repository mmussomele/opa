// Copyright 2016 The OPA Authors.  All rights reserved.
// Use of this source code is governed by an Apache2
// license that can be found in the LICENSE file.

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"crypto/tls"

	"net/url"

	"github.com/gorilla/mux"
	"github.com/open-policy-agent/opa/ast"
	"github.com/open-policy-agent/opa/metrics"
	"github.com/open-policy-agent/opa/server/authorizer"
	"github.com/open-policy-agent/opa/server/identifier"
	"github.com/open-policy-agent/opa/server/types"
	"github.com/open-policy-agent/opa/storage"
	"github.com/open-policy-agent/opa/topdown"
	"github.com/open-policy-agent/opa/topdown/explain"
	"github.com/open-policy-agent/opa/util"
	"github.com/open-policy-agent/opa/version"
	"github.com/pkg/errors"
)

// AuthenticationScheme enumerates the supported authentication schemes. The
// authentication scheme determines how client identities are established.
type AuthenticationScheme int

// Set of supported authentication schemes.
const (
	AuthenticationOff   AuthenticationScheme = iota
	AuthenticationToken                      = iota
)

// AuthorizationScheme enumerates the supported authorization schemes. The authorization
// scheme determines how access to OPA is controlled.
type AuthorizationScheme int

// Set of supported authorization schemes.
const (
	AuthorizationOff   AuthorizationScheme = iota
	AuthorizationBasic                     = iota
)

// Server represents an instance of OPA running in server mode.
type Server struct {
	Handler http.Handler

	addr           string
	insecureAddr   string
	authentication AuthenticationScheme
	authorization  AuthorizationScheme
	cert           *tls.Certificate
	mtx            sync.RWMutex
	compiler       *ast.Compiler
	store          *storage.Storage
}

// request represents a request that can be made to the server.
type request struct {
	ctx            context.Context
	values         url.Values
	path           string
	input          io.ReadCloser
	noneMatch      string
	getSource      bool
	pretty         bool
	explainMode    types.ExplainModeV1
	includeMetrics bool
}

const (
	CodeOK = iota
	CodeNotFound
	CodeBadRequest
	CodeNoContent
	CodeNotModified
)

// response represents the result of executing a request on the server.
type response struct {
	data   interface{}
	code   int
	msg    string
	err    error
	pretty bool
}

// New returns a new Server.
func New() *Server {

	s := Server{}

	// Initialize HTTP handlers.
	router := mux.NewRouter()
	s.registerHandler(router, 0, "/data/{path:.+}", "POST", httpV0DataPost(&s))
	s.registerHandler(router, 0, "/data", "POST", httpV0DataPost(&s))
	s.registerHandler(router, 1, "/data/{path:.+}", "PUT", httpV1DataPut(&s))
	s.registerHandler(router, 1, "/data", "PUT", httpV1DataPut(&s))
	s.registerHandler(router, 1, "/data/{path:.+}", "GET", httpV1DataGet(&s))
	s.registerHandler(router, 1, "/data", "GET", httpV1DataGet(&s))
	s.registerHandler(router, 1, "/data/{path:.+}", "PATCH", httpV1DataPatch(&s))
	s.registerHandler(router, 1, "/data", "PATCH", httpV1DataPatch(&s))
	s.registerHandler(router, 1, "/data/{path:.+}", "POST", httpV1DataPost(&s))
	s.registerHandler(router, 1, "/data", "POST", httpV1DataPost(&s))
	s.registerHandler(router, 1, "/policies", "GET", httpV1PoliciesList(&s))
	s.registerHandler(router, 1, "/policies/{path:.+}", "DELETE", httpV1PoliciesDelete(&s))
	s.registerHandler(router, 1, "/policies/{path:.+}", "GET", httpV1PoliciesGet(&s))
	s.registerHandler(router, 1, "/policies/{path:.+}", "PUT", httpV1PoliciesPut(&s))
	s.registerHandler(router, 1, "/query", "GET", httpV1QueryGet(&s))
	router.HandleFunc("/", s.indexGet).Methods("GET")
	s.Handler = router

	return &s
}

// Init initializes the server. This function MUST be called before Loop.
func (s *Server) Init(ctx context.Context) (*Server, error) {

	// Add authorization handler. This must come BEFORE authentication handler
	// so that the latter can run first.
	switch s.authorization {
	case AuthorizationBasic:
		s.Handler = authorizer.NewBasic(s.Handler, s.Compiler, s.store)
	}

	switch s.authentication {
	case AuthenticationToken:
		s.Handler = identifier.NewTokenBased(s.Handler)
	}

	// Load policies from storage and initialize server's compiler.
	txn, err := s.store.NewTransaction(ctx)
	if err != nil {
		return nil, err
	}

	defer s.store.Close(ctx, txn)
	modules := s.store.ListPolicies(txn)
	compiler := ast.NewCompiler()
	if compiler.Compile(modules); compiler.Failed() {
		return nil, compiler.Errors
	}

	s.setCompiler(compiler)

	return s, nil
}

// WithAddress sets the listening address that the server will bind to.
func (s *Server) WithAddress(addr string) *Server {
	s.addr = addr
	return s
}

// WithInsecureAddress sets the listening address that the server will bind to.
func (s *Server) WithInsecureAddress(addr string) *Server {
	s.insecureAddr = addr
	return s
}

// WithAuthentication sets authentication scheme to use on the server.
func (s *Server) WithAuthentication(scheme AuthenticationScheme) *Server {
	s.authentication = scheme
	return s
}

// WithAuthorization sets authorization scheme to use on the server.
func (s *Server) WithAuthorization(scheme AuthorizationScheme) *Server {
	s.authorization = scheme
	return s
}

// WithCertificate sets the server-side certificate that the server will use.
func (s *Server) WithCertificate(cert *tls.Certificate) *Server {
	s.cert = cert
	return s
}

// WithStorage sets the storage used by the server.
func (s *Server) WithStorage(store *storage.Storage) *Server {
	s.store = store
	return s
}

// Compiler returns the server's compiler.
//
// The server's compiler contains the compiled versions of all modules added to
// the server as well as data structures for performing query analysis. This is
// intended to allow services to embed the OPA server while still relying on the
// topdown package for query evaluation.
func (s *Server) Compiler() *ast.Compiler {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	return s.compiler
}

// Listeners returns functions that listen and serve connections.
func (s *Server) Listeners() (func() error, func() error) {

	server1 := http.Server{
		Addr:    s.addr,
		Handler: s.Handler,
	}

	loop1 := func() error { return server1.ListenAndServe() }

	if s.cert == nil {
		return loop1, nil
	}

	server2 := http.Server{
		Addr:    s.addr,
		Handler: s.Handler,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{*s.cert},
		},
	}

	loop2 := func() error { return server2.ListenAndServeTLS("", "") }

	if s.insecureAddr == "" {
		return loop2, nil
	}

	server1.Addr = s.insecureAddr

	return loop2, loop1
}

func (s *Server) execQuery(ctx context.Context, compiler *ast.Compiler, txn storage.Transaction, query ast.Body, input ast.Value, explainMode types.ExplainModeV1) (types.QueryResponseV1, error) {

	t := topdown.New(ctx, query, s.Compiler(), s.store, txn).WithInput(input)

	var buf *topdown.BufferTracer

	if explainMode != types.ExplainOffV1 {
		buf = topdown.NewBufferTracer()
		t.Tracer = buf
	}

	qrs := types.AdhocQueryResultSetV1{}

	err := topdown.Eval(t, func(t *topdown.Topdown) error {
		result := map[string]interface{}{}
		for k, v := range t.Vars() {
			if !k.IsWildcard() {
				x, err := ast.ValueToInterface(v, t)
				if err != nil {
					return err
				}
				result[k.String()] = x
			}
		}
		if len(result) > 0 {
			qrs = append(qrs, result)
		}
		return nil
	})

	if err != nil {
		return types.QueryResponseV1{}, err
	}

	result := types.QueryResponseV1{
		Result: qrs,
	}

	switch explainMode {
	case types.ExplainFullV1:
		result.Explanation = types.NewTraceV1(*buf)
	case types.ExplainTruthV1:
		answer, err := explain.Truth(compiler, *buf)
		if err != nil {
			return types.QueryResponseV1{}, err
		}
		result.Explanation = types.NewTraceV1(answer)
	}

	return result, nil
}

func (s *Server) indexGet(w http.ResponseWriter, r *http.Request) {

	renderHeader(w)
	renderBanner(w)
	renderVersion(w)
	defer renderFooter(w)

	ctx := r.Context()
	values := r.URL.Query()
	qStrs := values["q"]
	inputStrs := values["input"]
	explainMode := getExplain(r.URL.Query()["explain"])

	renderQueryForm(w, qStrs, inputStrs, explainMode)

	if len(qStrs) == 0 {
		return
	}

	qStr := qStrs[len(qStrs)-1]
	t0 := time.Now()

	var results interface{}
	txn, err := s.store.NewTransaction(ctx)

	if err != nil {
		renderQueryResult(w, nil, err, t0)
		return
	}

	defer s.store.Close(ctx, txn)

	var query ast.Body
	query, err = ast.ParseBody(qStr)
	if err != nil {
		renderQueryResult(w, nil, err, t0)
		return
	}

	var input ast.Value

	if len(inputStrs) > 0 {
		inputStr := inputStrs[len(qStrs)-1]
		t, err := ast.ParseTerm(inputStr)
		if err != nil {
			renderQueryResult(w, nil, err, t0)
			return
		}
		input = t.Value
	}

	compiler := s.Compiler()
	queryContext := ast.NewQueryContext().WithInput(input)
	query, err = compiler.QueryCompiler().WithContext(queryContext).Compile(query)
	if err != nil {
		renderQueryResult(w, nil, err, t0)
		return
	}

	results, err = s.execQuery(ctx, compiler, txn, query, input, explainMode)
	if err != nil {
		renderQueryResult(w, nil, err, t0)
		return
	}

	renderQueryResult(w, results, err, t0)
}

func (s *Server) registerHandler(router *mux.Router, version int, path string, method string, h func(http.ResponseWriter, *http.Request)) {
	prefix := fmt.Sprintf("/v%d", version)
	router.HandleFunc(prefix+path, h).Methods(method)
}

func (s *Server) v1DataGet(r request) (resp response) {
	path := stringPathToDataRef(r.path)

	m := metrics.New()
	m.Timer(metrics.RegoQueryParse).Start()

	input, nonGround, err := readInputParam(r.values[types.ParamInputV1])
	if err != nil {
		resp.code = CodeBadRequest
		resp.msg = types.CodeInvalidParameter
		resp.err = err
		return
	}

	m.Timer(metrics.RegoQueryParse).Stop()

	if nonGround && r.explainMode != types.ExplainOffV1 {
		resp.code = CodeBadRequest
		resp.data = types.NewErrorV1(types.CodeInvalidParameter, "not supported: explanations with non-ground input values").Bytes()
		return
	}

	// Prepare for query.
	txn, err := s.store.NewTransaction(r.ctx)
	if err != nil {
		resp.err = err
		return
	}

	defer s.store.Close(r.ctx, txn)

	compiler := s.Compiler()
	params := topdown.NewQueryParams(r.ctx, compiler, s.store, txn, input, path)
	params.Metrics = m

	var buf *topdown.BufferTracer
	if r.explainMode != types.ExplainOffV1 {
		buf = topdown.NewBufferTracer()
		params.Tracer = buf
	}

	// Execute query.
	qrs, err := topdown.Query(params)

	// Handle results.
	if err != nil {
		resp.err = err
		return
	}

	result := types.DataResponseV1{}

	if r.includeMetrics {
		result.Metrics = m.All()
	}

	if qrs.Undefined() {
		if r.explainMode == types.ExplainFullV1 {
			result.Explanation = types.NewTraceV1(*buf)
		}
		resp.code = CodeOK
		resp.data = result
		resp.pretty = r.pretty
		return
	}

	if nonGround {
		var i interface{} = types.NewQueryResultSetV1(qrs)
		result.Result = &i
	} else {
		result.Result = &qrs[0].Result
	}

	switch r.explainMode {
	case types.ExplainFullV1:
		result.Explanation = types.NewTraceV1(*buf)
	case types.ExplainTruthV1:
		answer, err := explain.Truth(compiler, *buf)
		if err != nil {
			resp.err = err
			return
		}
		result.Explanation = types.NewTraceV1(answer)
	}

	resp.code = CodeOK
	resp.data = result
	resp.pretty = r.pretty
	return
}

func (s *Server) v1DataPatch(r request) (resp response) {
	ops := []types.PatchV1{}

	if err := util.NewJSONDecoder(r.input).Decode(&ops); err != nil {
		resp.code = CodeBadRequest
		resp.msg = types.CodeInvalidParameter
		resp.err = err
		return
	}

	txn, err := s.store.NewTransaction(r.ctx)
	if err != nil {
		resp.err = err
		return
	}

	defer s.store.Close(r.ctx, txn)

	patches, err := s.prepareV1PatchSlice(r.path, ops)
	if err != nil {
		resp.err = err
		return
	}

	for _, patch := range patches {
		if err := s.store.Write(r.ctx, txn, patch.op, patch.path, patch.value); err != nil {
			resp.err = err
			return
		}
	}

	resp.code = CodeNoContent
	return
}

func (s *Server) v0DataPost(r request) (resp response) {
	path := stringPathToDataRef(r.path)
	input, err := readInputV0(r.input)
	if err != nil {
		resp.code = CodeBadRequest
		resp.msg = types.CodeInvalidParameter
		resp.err = errors.Wrapf(err, "unexpected parse error for input")
		return
	}

	// Prepare for query.
	txn, err := s.store.NewTransaction(r.ctx)
	if err != nil {
		resp.err = err
		return
	}

	defer s.store.Close(r.ctx, txn)

	compiler := s.Compiler()
	params := topdown.NewQueryParams(r.ctx, compiler, s.store, txn, input, path)

	// Execute query.
	qrs, err := topdown.Query(params)

	// Handle results.
	if err != nil {
		resp.err = err
		return
	}

	if qrs.Undefined() {
		resp.code = CodeNotFound
		resp.data = types.NewErrorV1(types.CodeUndefinedDocument, fmt.Sprintf("%v: %v", types.MsgUndefinedError, path)).Bytes()
		return
	}

	resp.code = CodeOK
	resp.data = qrs[0].Result
	resp.pretty = false
	return
}

func (s *Server) v1DataPost(r request) (resp response) {
	path := stringPathToDataRef(r.path)

	m := metrics.New()
	m.Timer(metrics.RegoQueryParse).Start()

	input, err := readInputV1(r.input)
	if err != nil {
		resp.code = CodeBadRequest
		resp.msg = types.CodeInvalidParameter
		resp.err = err
		return
	}

	m.Timer(metrics.RegoQueryParse).Stop()

	// Prepare for query.
	txn, err := s.store.NewTransaction(r.ctx)
	if err != nil {
		resp.err = err
		return
	}

	defer s.store.Close(r.ctx, txn)

	compiler := s.Compiler()
	params := topdown.NewQueryParams(r.ctx, compiler, s.store, txn, input, path)

	params.Metrics = m

	var buf *topdown.BufferTracer
	if r.explainMode != types.ExplainOffV1 {
		buf = topdown.NewBufferTracer()
		params.Tracer = buf
	}

	// Execute query.
	qrs, err := topdown.Query(params)

	// Handle results.
	if err != nil {
		resp.err = err
		return
	}

	result := types.DataResponseV1{}

	if r.includeMetrics {
		result.Metrics = m.All()
	}

	if qrs.Undefined() {
		if r.explainMode == types.ExplainFullV1 {
			result.Explanation = types.NewTraceV1(*buf)
		}
		resp.code = CodeOK
		resp.data = result
		resp.pretty = r.pretty
		return
	}

	result.Result = &qrs[0].Result

	switch r.explainMode {
	case types.ExplainFullV1:
		result.Explanation = types.NewTraceV1(*buf)
	case types.ExplainTruthV1:
		answer, err := explain.Truth(compiler, *buf)
		if err != nil {
			resp.err = err
			return
		}
		result.Explanation = types.NewTraceV1(answer)
	}

	resp.code = CodeOK
	resp.data = result
	resp.pretty = r.pretty
	return
}

func (s *Server) v1DataPut(r request) (resp response) {
	var value interface{}
	if err := util.NewJSONDecoder(r.input).Decode(&value); err != nil {
		resp.code = CodeBadRequest
		resp.msg = types.CodeInvalidParameter
		resp.err = err
		return
	}

	txn, err := s.store.NewTransaction(r.ctx)
	if err != nil {
		resp.err = err
		return
	}

	defer s.store.Close(r.ctx, txn)

	path, ok := storage.ParsePath("/" + strings.Trim(r.path, "/"))
	if !ok {
		resp.code = CodeBadRequest
		resp.err = fmt.Errorf("%s: bad path: %v", types.CodeInvalidParameter, r.path)
		return
	}

	_, err = s.store.Read(r.ctx, txn, path)

	if err != nil {
		if !storage.IsNotFound(err) {
			resp.err = err
			return
		}
		if err := s.makeDir(r.ctx, txn, path[:len(path)-1]); err != nil {
			resp.err = err
			return
		}
	} else if r.noneMatch == "*" {
		resp.code = CodeNotModified
		return
	}

	if err := s.store.Write(r.ctx, txn, storage.AddOp, path, value); err != nil {
		resp.err = err
		return
	}

	resp.code = CodeNoContent
	return
}

func (s *Server) v1PoliciesDelete(r request) (resp response) {
	id := r.path
	txn, err := s.store.NewTransaction(r.ctx)
	if err != nil {
		resp.err = err
		return
	}

	defer s.store.Close(r.ctx, txn)

	_, _, err = s.store.GetPolicy(txn, id)
	if err != nil {
		resp.err = err
		return
	}

	mods := s.store.ListPolicies(txn)
	delete(mods, id)

	c := ast.NewCompiler()

	if c.Compile(mods); c.Failed() {
		resp.code = CodeBadRequest
		resp.data = types.NewErrorV1(types.CodeInvalidOperation, types.MsgCompileModuleError).WithASTErrors(c.Errors).Bytes()
		return
	}

	if err := s.store.DeletePolicy(txn, id); err != nil {
		resp.err = err
		return
	}

	s.setCompiler(c)

	resp.code = CodeNoContent
	return
}

func (s *Server) v1PoliciesGet(r request) (resp response) {
	txn, err := s.store.NewTransaction(r.ctx)
	if err != nil {
		resp.err = err
		return
	}

	defer s.store.Close(r.ctx, txn)

	_, bs, err := s.store.GetPolicy(txn, r.path)
	if err != nil {
		resp.err = err
		return
	}

	if r.getSource {
		resp.code = CodeOK
		resp.data = bs
		return
	}

	c := s.Compiler()

	response := types.PolicyGetResponseV1{
		Result: types.PolicyV1{
			ID:     r.path,
			Module: c.Modules[r.path],
		},
	}

	resp.code = CodeOK
	resp.data = response
	resp.pretty = true
	return
}

func (s *Server) v1PoliciesList(r request) (resp response) {
	policies := []types.PolicyV1{}

	c := s.Compiler()
	for id, mod := range c.Modules {
		policy := types.PolicyV1{
			ID:     id,
			Module: mod,
		}
		policies = append(policies, policy)
	}

	response := types.PolicyListResponseV1{
		Result: policies,
	}

	resp.code = CodeOK
	resp.data = response
	resp.pretty = true
	return
}

func (s *Server) v1PoliciesPut(r request) (resp response) {
	buf, err := ioutil.ReadAll(r.input)
	if err != nil {
		resp.code = CodeBadRequest
		resp.msg = types.CodeInvalidParameter
		resp.err = err
		return
	}

	parsedMod, err := ast.ParseModule(r.path, string(buf))
	if err != nil {
		resp.code = CodeBadRequest
		switch err := err.(type) {
		case ast.Errors:
			resp.data = types.NewErrorV1(types.CodeInvalidParameter, types.MsgCompileModuleError).WithASTErrors(err).Bytes()
		default:
			resp.msg = types.CodeInvalidParameter
			resp.err = err
		}
		return
	}

	if parsedMod == nil {
		resp.code = CodeBadRequest
		resp.data = types.NewErrorV1(types.CodeInvalidParameter, "empty module").Bytes()
		return
	}

	txn, err := s.store.NewTransaction(r.ctx)

	if err != nil {
		resp.err = err
		return
	}

	defer s.store.Close(r.ctx, txn)

	mods := s.store.ListPolicies(txn)
	mods[r.path] = parsedMod

	c := ast.NewCompiler()
	if c.Compile(mods); c.Failed() {
		resp.code = CodeBadRequest
		resp.data = types.NewErrorV1(types.CodeInvalidParameter, types.MsgCompileModuleError).WithASTErrors(c.Errors).Bytes()
		return
	}

	if err := s.store.InsertPolicy(txn, r.path, parsedMod, buf); err != nil {
		resp.err = err
		return
	}

	s.setCompiler(c)
	response := types.PolicyPutResponseV1{
		Result: types.PolicyV1{
			ID:     r.path,
			Module: c.Modules[r.path],
		},
	}

	resp.code = CodeOK
	resp.data = response
	resp.pretty = true
	return
}

func (s *Server) v1QueryGet(r request) (resp response) {
	m := metrics.New()
	qStrs := r.values["q"]
	if len(qStrs) == 0 {
		resp.code = CodeBadRequest
		resp.data = types.NewErrorV1(types.CodeInvalidParameter, "missing parameter 'q'").Bytes()
		return
	}

	qStr := qStrs[len(qStrs)-1]
	txn, err := s.store.NewTransaction(r.ctx)
	if err != nil {
		resp.err = err
		return
	}

	defer s.store.Close(r.ctx, txn)

	compiler := s.Compiler()

	m.Timer(metrics.RegoQueryParse).Start()

	query, err := ast.ParseBody(qStr)
	if err != nil {
		return handleCompileError(err)
	}

	m.Timer(metrics.RegoQueryParse).Stop()
	m.Timer(metrics.RegoQueryCompile).Start()

	compiled, err := compiler.QueryCompiler().Compile(query)
	if err != nil {
		return handleCompileError(err)
	}

	m.Timer(metrics.RegoQueryCompile).Stop()
	m.Timer(metrics.RegoQueryEval).Start()

	results, err := s.execQuery(r.ctx, compiler, txn, compiled, nil, r.explainMode)
	if err != nil {
		resp.err = err
		return
	}

	m.Timer(metrics.RegoQueryEval).Stop()

	if r.includeMetrics {
		results.Metrics = m.All()
	}

	resp.code = CodeOK
	resp.data = results
	resp.pretty = r.pretty
	return
}

func handleCompileError(err error) (resp response) {
	resp.code = CodeBadRequest
	switch err := err.(type) {
	case ast.Errors:
		resp.data = types.NewErrorV1(types.CodeInvalidParameter, types.MsgCompileQueryError).WithASTErrors(err).Bytes()
	default:
		resp.msg = types.CodeInvalidParameter
		resp.err = err
	}
	return
}

func (s *Server) setCompiler(compiler *ast.Compiler) {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	s.compiler = compiler
}

func (s *Server) makeDir(ctx context.Context, txn storage.Transaction, path storage.Path) error {

	node, err := s.store.Read(ctx, txn, path)
	if err == nil {
		if _, ok := node.(map[string]interface{}); ok {
			return nil
		}
		return types.WriteConflictErr{
			Path: path,
		}
	}

	if !storage.IsNotFound(err) {
		return err
	}

	if err := s.makeDir(ctx, txn, path[:len(path)-1]); err != nil {
		return err
	}

	if err := s.writeConflict(storage.AddOp, path); err != nil {
		return err
	}

	return s.store.Write(ctx, txn, storage.AddOp, path, map[string]interface{}{})
}

func (s *Server) prepareV1PatchSlice(root string, ops []types.PatchV1) (result []patchImpl, err error) {

	root = "/" + strings.Trim(root, "/")

	for _, op := range ops {

		impl := patchImpl{
			value: op.Value,
		}

		// Map patch operation.
		switch op.Op {
		case "add":
			impl.op = storage.AddOp
		case "remove":
			impl.op = storage.RemoveOp
		case "replace":
			impl.op = storage.ReplaceOp
		default:
			return nil, types.BadPatchOperationErr(op.Op)
		}

		// Construct patch path.
		path := strings.Trim(op.Path, "/")
		if len(path) > 0 {
			path = root + "/" + path
		} else {
			path = root
		}

		var ok bool
		impl.path, ok = storage.ParsePath(path)
		if !ok {
			return nil, types.BadPatchPathErr(op.Path)
		}

		if err := s.writeConflict(impl.op, impl.path); err != nil {
			return nil, err
		}

		result = append(result, impl)
	}

	return result, nil
}

// TODO(tsandall): this ought to be enforced by the storage layer.
func (s *Server) writeConflict(op storage.PatchOp, path storage.Path) error {

	if op == storage.AddOp && len(path) > 0 && path[len(path)-1] == "-" {
		path = path[:len(path)-1]
	}

	ref := path.Ref(ast.DefaultRootDocument)

	if rs := s.Compiler().GetRulesForVirtualDocument(ref); rs != nil {
		return types.WriteConflictErr{
			Path: path,
		}
	}

	return nil
}

func stringPathToDataRef(s string) (r ast.Ref) {
	result := ast.Ref{ast.DefaultRootDocument}
	result = append(result, stringPathToRef(s)...)
	return result
}

func stringPathToRef(s string) (r ast.Ref) {
	if len(s) == 0 {
		return r
	}
	p := strings.Split(s, "/")
	for _, x := range p {
		if x == "" {
			continue
		}
		i, err := strconv.Atoi(x)
		if err != nil {
			r = append(r, ast.StringTerm(x))
		} else {
			r = append(r, ast.IntNumberTerm(i))
		}
	}
	return r
}

func getBoolParam(url *url.URL, name string, ifEmpty bool) bool {

	p, ok := url.Query()[name]
	if !ok {
		return false
	}

	// Query params w/o values are represented as slice (of len 1) with an
	// empty string.
	if len(p) == 1 && p[0] == "" {
		return ifEmpty
	}

	for _, x := range p {
		if strings.ToLower(x) == "true" {
			return true
		}
	}

	return false
}

func getExplain(p []string) types.ExplainModeV1 {
	for _, x := range p {
		switch x {
		case string(types.ExplainFullV1):
			return types.ExplainFullV1
		case string(types.ExplainTruthV1):
			return types.ExplainTruthV1
		}
	}
	return types.ExplainOffV1
}

var errInputPathFormat = fmt.Errorf(`input parameter format is [[<path>]:]<value> where <path> is either var or ref`)

func readInputV0(r io.ReadCloser) (ast.Value, error) {

	bs, err := ioutil.ReadAll(r)

	if err != nil {
		return nil, err
	}

	s := strings.TrimSpace(string(bs))
	if len(s) == 0 {
		return nil, nil
	}

	term, err := ast.ParseTerm(s)
	if err != nil {
		return nil, err
	}

	return term.Value, nil
}

func readInputV1(r io.ReadCloser) (ast.Value, error) {

	bs, err := ioutil.ReadAll(r)

	if err != nil {
		return nil, err
	}

	var input ast.Value

	if len(bs) > 0 {

		var request types.DataRequestV1

		if err := util.UnmarshalJSON(bs, &request); err != nil {
			return nil, errors.Wrapf(err, "body contains malformed input document")
		}

		if request.Input == nil {
			return nil, fmt.Errorf(types.MsgInputDocError)
		}

		var err error
		input, err = ast.InterfaceToValue(*request.Input)
		if err != nil {
			return nil, err
		}
	}

	return input, nil
}

func readInputParam(s []string) (ast.Value, bool, error) {

	pairs := make([][2]*ast.Term, len(s))
	nonGround := false

	for i := range s {

		var k *ast.Term
		var v *ast.Term
		var err error

		if len(s[i]) == 0 {
			return nil, false, errInputPathFormat
		}

		if s[i][0] == ':' {
			k = ast.NewTerm(ast.InputRootRef)
			v, err = ast.ParseTerm(s[i][1:])
		} else {
			v, err = ast.ParseTerm(s[i])
			if err == nil {
				k = ast.NewTerm(ast.InputRootRef)
			} else {
				vs := strings.SplitN(s[i], ":", 2)
				if len(vs) != 2 {
					return nil, false, errInputPathFormat
				}
				k, err = ast.ParseTerm(ast.InputRootDocument.String() + "." + vs[0])
				if err != nil {
					return nil, false, errInputPathFormat
				}
				v, err = ast.ParseTerm(vs[1])
			}
		}

		if err != nil {
			return nil, false, err
		}

		pairs[i] = [...]*ast.Term{k, v}

		if !nonGround {
			ast.WalkVars(v, func(x ast.Var) bool {
				if x.Equal(ast.DefaultRootDocument.Value) {
					return false
				}
				nonGround = true
				return true
			})
		}
	}

	input, err := topdown.MakeInput(pairs)
	if err != nil {
		return nil, false, err
	}

	return input, nonGround, nil
}

func renderBanner(w http.ResponseWriter) {
	fmt.Fprintln(w, `<pre>
 ________      ________    ________
|\   __  \    |\   __  \  |\   __  \
\ \  \|\  \   \ \  \|\  \ \ \  \|\  \
 \ \  \\\  \   \ \   ____\ \ \   __  \
  \ \  \\\  \   \ \  \___|  \ \  \ \  \
   \ \_______\   \ \__\      \ \__\ \__\
    \|_______|    \|__|       \|__|\|__|
	</pre>`)
	fmt.Fprintln(w, "Open Policy Agent - An open source project to policy-enable your service.<br>")
	fmt.Fprintln(w, "<br>")
}

func renderFooter(w http.ResponseWriter) {
	fmt.Fprintln(w, "</body>")
	fmt.Fprintln(w, "</html>")
}

func renderHeader(w http.ResponseWriter) {
	fmt.Fprintln(w, "<html>")
	fmt.Fprintln(w, "<body>")
}

func renderQueryForm(w http.ResponseWriter, qStrs []string, inputStrs []string, explain types.ExplainModeV1) {

	query := ""

	if len(qStrs) > 0 {
		query = qStrs[len(qStrs)-1]
	}

	input := ""
	if len(inputStrs) > 0 {
		input = inputStrs[len(inputStrs)-1]
	}

	explainRadioCheck := []string{"", "", ""}
	switch explain {
	case types.ExplainOffV1:
		explainRadioCheck[0] = "checked"
	case types.ExplainFullV1:
		explainRadioCheck[1] = "checked"
	case types.ExplainTruthV1:
		explainRadioCheck[2] = "checked"
	}

	fmt.Fprintf(w, `
	<form>
  	Query:<br>
	<textarea rows="10" cols="50" name="q">%s</textarea><br>
	<br>Input Data (JSON):<br>
	<textarea rows="10" cols="50" name="input">%s</textarea><br>
	<br><input type="submit" value="Submit"> Explain:
	<input type="radio" name="explain" value="off" %v>Off
	<input type="radio" name="explain" value="full" %v>Full
	<input type="radio" name="explain" value="truth" %v>Truth
	</form>`, query, input, explainRadioCheck[0], explainRadioCheck[1], explainRadioCheck[2])
}

func renderQueryResult(w io.Writer, results interface{}, err error, t0 time.Time) {

	buf, err2 := json.MarshalIndent(results, "", "  ")
	d := time.Since(t0)

	if err != nil {
		fmt.Fprintf(w, "Query error (took %v): <pre>%v</pre>", d, err)
	} else if err2 != nil {
		fmt.Fprintf(w, "JSON marshal error: <pre>%v</pre>", err2)
	} else {
		fmt.Fprintf(w, "Query results (took %v):<br>", d)
		fmt.Fprintf(w, "<pre>%s</pre>", string(buf))
	}
}

func renderVersion(w http.ResponseWriter) {
	fmt.Fprintln(w, "Version: "+version.Version+"<br>")
	fmt.Fprintln(w, "Build Commit: "+version.Vcs+"<br>")
	fmt.Fprintln(w, "Build Timestamp: "+version.Timestamp+"<br>")
	fmt.Fprintln(w, "Build Hostname: "+version.Hostname+"<br>")
	fmt.Fprintln(w, "<br>")
}

type patchImpl struct {
	path  storage.Path
	op    storage.PatchOp
	value interface{}
}
