# MapReduceIndex-Demo

Paths for the modified files are:-
modified:   common/index.go
modified:   indexer/kv_sender.go
modified:   protobuf/projector/index.pb.go
modified:   protobuf/projector/projector.go

New Files added :-

protobuf/projector/JSEvaluate.go   
protobuf/projector/indexjs.go     #implements evaluator interface


Building v8 -> Change CXXFLAGS and LDFLAGS in JSEvaluate.go to point to the static library of libCGOTRY.a (path of libcgotry) and v8 libraries
