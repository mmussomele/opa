package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"io/ioutil"
	"strings"

	pb "github.com/open-policy-agent/opa/server/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type opaServer struct {
	server *Server
}

func (s opaServer) V0DataPost(ctx context.Context, r *pb.V0DataPostRequest) (*pb.V0DataPostResponse, error) {
	req := request{
		path:   r.Path,
		input:  makeReadCloser(r.Input),
		pretty: r.Pretty,
	}

	resp := errToData(s.server.v0DataPost(req))
	pbResp := &pb.V0DataPostResponse{
		Code:   resp.code,
		Result: marshalToString(resp.data),
	}
}

func (s opaServer) V1DataPut(ctx context.Context, r *pb.V1DataPutRequest) (*pb.V1DataPutResponse, error) {
}

func (s opaServer) V1DataGet(ctx context.Context, r *pb.V1DataGetRequest) (*pb.V1DataGetResponse, error) {
}

func (s opaServer) V1DataPatch(ctx context.Context, r *pb.V1DataPatchRequest) (*pb.V1DataPatchResponse, error) {
}

func (s opaServer) V1DataPost(ctx context.Context, r *pb.V1DataPostRequest) (*pb.V1DataPostResponse, error) {
}

func (s opaServer) V1QueryGet(ctx context.Context, r *pb.V1QueryGetRequest) (*pb.V1QueryGetResponse, error) {
}

func (s opaServer) V1PoliciesPut(ctx context.Context, r *pb.V1PoliciesPutRequest) (*pb.V1PoliciesPutResponse, error) {
}

func (s opaServer) V1PoliciesList(ctx context.Context, r *pb.V1PoliciesListRequest) (*pb.V1PoliciesListResponse, error) {
}

func (s opaServer) V1PoliciesGet(ctx context.Context, r *pb.V1PoliciesGetRequest) (*pb.V1PoliciesGetResponse, error) {
}

func (s opaServer) V1PoliciesDelete(ctx context.Context, r *pb.V1PoliciesDeleteRequest) (*pb.V1PoliciesDeleteResponse, error) {
}

func makeReadCloser(s string) io.ReadCloser {
	return ioutil.NopCloser(strings.NewReader(s))
}

func marshalToString(v interface{}, pretty bool) string {
	if s, ok := v.(string); ok {
		return s
	}

	var bs []byte
	var err error
	if pretty {
		bs, err = json.MarshalIndent(v, "", "  ")
	} else {
		bs, err = json.Marshal(v)
	}

	if err != nil {
		return ""
	}
	return string(bs)
}

// All gRPC functions return a status code and some of them a string result.
// This function boils all the information in a server response down to the
// code and data fields for easy construction into a gRPC response.
func errToData(r response) response {
	// If there was no error, then the code and data fields are already
	// properly populated.
	if r.err == nil {
		return r
	}

	// If a message was provided, we're dealing with a generic error.
	if r.msg != "" {
		return response{
			code: r.code,
			data: types.NewErrorV1(r.msg, err.Error()).Error(),
		}
	}

	if terr, ok := r.err.(*types.ErrorV1); ok {
		return response{
			code: r.code,
			data: terr.Error(),
		}
	}

	if types.IsBadRequest(r.err) {
		return response{
			code: CodeBadRequest,
			data: types.NewErrorV1(types.CodeInvalidParameter, r.err.Error()).Error(),
		}
	}

	if types.IsWriteConflict(r.err) {
		return response{
			code: CodeNotFound,
			data: types.NewErrorV1(types.CodeResourceConflict, r.err.Error()).Error(),
		}
	}

	if topdown.IsError(r.err) {
		return response{
			code: CodeServerError,
			data: types.NewErrorV1(types.CodeInternal, types.MsgEvaluationError).WithError(r.err).Error(),
		}
	}

	if ast.IsError(ast.InputErr, r.err) {
		return response{
			code: CodeBadRequest,
			data: types.NewErrorV1(types.CodeInvalidParameter, types.MsgInputDocError).WithError(r.err).Error(),
		}
	}

	if storage.IsInvalidPatch(r.err) {
		return response{
			code: CodeBadRequest,
			data: types.NewErrorV1(types.CodeInvalidParameter, r.err.Error()).Error(),
		}
	}

	if storage.IsNotFound(r.err) {
		ErrorString(w, http.StatusNotFound, types.CodeResourceNotFound, r.err)
		return response{
			code: CodeNotFound,
			data: types.NewErrorV1(types.CodeResourceNotFound, r.err.Error()).Error(),
		}
	}

	return response{
		code: CodeServerError,
		data: types.NewErrorV1(types.CodeInternal, r.err.Error()).Error(),
	}
}
