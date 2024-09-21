package httpovergrpc

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/Jille/httpovergrpc/proto/pb"
	"google.golang.org/grpc"
)

// Register a gRPC service that forwards incoming HTTP-over-gRPC to the given handler.
func Register(s grpc.ServiceRegistrar, handler http.Handler) {
	pb.RegisterHTTPOverGRPCServiceServer(s, receiver{handler})
}

type receiver struct {
	handler http.Handler
}

func (s receiver) HTTP(stream pb.HTTPOverGRPCService_HTTPServer) error {
	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	msg, err := stream.Recv()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, msg.GetMethod(), msg.GetUrl(), nil)
	if err != nil {
		return err
	}
	req.Proto = msg.GetProto()
	var ok bool
	if req.ProtoMajor, req.ProtoMinor, ok = http.ParseHTTPVersion(req.Proto); !ok {
		return fmt.Errorf("malformed HTTP version %q", req.Proto)
	}
	for _, h := range msg.GetHeaders() {
		req.Header[http.CanonicalHeaderKey(h.Header)] = h.Values
	}
	if n, err := strconv.ParseInt(req.Header.Get("Content-Length"), 10, 64); err == nil {
		req.ContentLength = n
	} else {
		req.ContentLength = -1
	}
	req.RemoteAddr = msg.GetRemoteAddr()

	rw := responseWriter{
		header: http.Header{},
		stream: stream,
		failed: cancel,
	}

	req.Body = &bodyReader{
		rw:             &rw,
		expectContinue: req.Header.Get("Expect") == "100-continue",
	}

	s.handler.ServeHTTP(&rw, req)
	rw.WriteHeader(200) // A noop if the header was already sent.
	return nil
}

type responseWriter struct {
	header http.Header
	failed func()

	writeMtx   sync.Mutex
	sentHeader bool
	stream     pb.HTTPOverGRPCService_HTTPServer
}

func (rw *responseWriter) Header() http.Header {
	return rw.header
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.writeMtx.Lock()
	defer rw.writeMtx.Unlock()
	if rw.sentHeader {
		return
	}
	rw.sentHeader = true
	msg := &pb.HTTPResponse{
		StatusCode: int32(code),
	}
	for h, vs := range rw.header {
		msg.Headers = append(msg.Headers, &pb.HTTPHeader{
			Header: h,
			Values: vs,
		})
	}
	if err := rw.stream.Send(msg); err != nil {
		rw.failed()
	}
}

func (rw *responseWriter) Write(p []byte) (int, error) {
	rw.WriteHeader(200) // A noop if the header was already sent.
	msg := &pb.HTTPResponse{
		BodyData: p,
	}
	rw.writeMtx.Lock()
	defer rw.writeMtx.Unlock()
	if err := rw.stream.Send(msg); err != nil {
		rw.failed()
		return 0, err
	}
	return len(p), nil
}

type bodyReader struct {
	rw             *responseWriter
	expectContinue bool
	buf            []byte
	err            error
}

func (br *bodyReader) Read(p []byte) (int, error) {
	if len(br.buf) > 0 {
		n := copy(p, br.buf)
		br.buf = br.buf[n:]
		return n, nil
	}
	if br.err != nil {
		return 0, br.err
	}
	if br.expectContinue {
		br.expectContinue = false
		br.rw.writeMtx.Lock()
		if !br.rw.sentHeader {
			if err := br.rw.stream.Send(&pb.HTTPResponse{StatusCode: 100}); err != nil {
				br.rw.failed()
				br.rw.writeMtx.Unlock()
				br.err = err
				return 0, err
			}
		}
		br.rw.writeMtx.Unlock()
	}
	for {
		msg, err := br.rw.stream.Recv()
		if err != nil {
			br.err = err
			if err != io.EOF {
				br.rw.failed()
			}
			return 0, err
		}
		n := copy(p, msg.GetBodyData())
		br.buf = msg.GetBodyData()[n:]
		return n, nil
	}
}

func (br *bodyReader) Close() error {
	br.err = io.ErrClosedPipe
	return nil
}
