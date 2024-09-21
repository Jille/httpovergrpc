package httpovergrpc

import (
	"context"
	"io"
	"net/http"

	"github.com/Jille/httpovergrpc/proto/pb"
	"google.golang.org/grpc"
)

// Forwarder creates a http.Handler that sends incoming requests over the given gRPC connection.
func Forwarder(s grpc.ClientConnInterface) http.Handler {
	return handler{pb.NewHTTPOverGRPCServiceClient(s)}
}

type handler struct {
	conn pb.HTTPOverGRPCServiceClient
}

func (h handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	stream, err := h.conn.HTTP(ctx)
	if err != nil {
		http.Error(w, "Upstream error", 502)
		return
	}
	freq := pb.HTTPRequest{
		Method:     req.Method,
		Url:        req.URL.String(),
		Proto:      req.Proto,
		RemoteAddr: req.RemoteAddr,
		Headers:    make([]*pb.HTTPHeader, 0, len(req.Header)),
	}
	for h, vs := range req.Header {
		freq.Headers = append(freq.Headers, &pb.HTTPHeader{
			Header: h,
			Values: vs,
		})
	}
	if err := stream.Send(&freq); err != nil {
		http.Error(w, "Upstream error", 502)
		return
	}
	if req.Header.Get("Expect") != "100-continue" {
		go transmitBody(stream, req, cancel)
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			http.Error(w, "Upstream error", 502)
			return
		}
		for _, h := range msg.GetHeaders() {
			w.Header()[http.CanonicalHeaderKey(h.Header)] = h.GetValues()
		}
		if msg.GetStatusCode() == 100 {
			go transmitBody(stream, req, cancel)
			continue
		}
		w.WriteHeader(int(msg.GetStatusCode()))
		break
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			return
		}
		if _, err := w.Write(msg.GetBodyData()); err != nil {
			return
		}
	}
}

func transmitBody(stream pb.HTTPOverGRPCService_HTTPClient, req *http.Request, failed func()) {
	buf := make([]byte, 4096)
	for {
		n, err := req.Body.Read(buf)
		if n > 0 {
			if err2 := stream.Send(&pb.HTTPRequest{
				BodyData: buf[:n],
			}); err2 != nil {
				failed()
				return
			}
		}
		if err != nil {
			if err == io.EOF {
				if err := stream.CloseSend(); err != nil {
					failed()
					return
				}
				return
			}
			failed()
			return
		}
	}
}
