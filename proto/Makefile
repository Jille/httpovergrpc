http.pb.go: http.proto Makefile
	protoc \
		--go_out=pb                                                     \
		--go_opt=paths=source_relative                                  \
		--go-grpc_out=pb                                                \
		--go-grpc_opt=paths=source_relative                             \
		--go-grpc_opt=require_unimplemented_servers=false               \
		http.proto
