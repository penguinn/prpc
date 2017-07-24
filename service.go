package prpc

import (
	"sync"
	"reflect"
)

type Conn struct {
	Codec 				Codec
	Data 				map[string]interface{}
}

type Header struct{
	IsResponse			bool
	ServiceMethod		string
	Seq 				uint64
	Error 				string
	next 				*Header
}

type Codec interface{
	ReadHeader(*Header) (error)
	ReadBody(interface{}) error
	Write(*Header, interface{}) error
	Close() error
}

type service struct {
	name   string                 // name of service
	rcvr   reflect.Value          // receiver of methods for the service
	typ    reflect.Type           // type of the receiver
	method map[string]*methodType // registered methods
}

func (s *service) Dispose(server *Server, sending *sync.Mutex, mtype *methodType, req *Header, argv, replyv reflect.Value, codec Codec) {
	s.dispose(server.Free, sending, mtype, req, argv, replyv, codec)
}
func (s *service) dispose(free Free, sending *sync.Mutex, mtype *methodType, req *Header, argv, replyv reflect.Value, codec Codec) {
	mtype.Lock()
	mtype.numCalls++
	mtype.Unlock()
	function := mtype.method.Func
	// Invoke the method, providing a new value for the reply.
	returnValues := function.Call([]reflect.Value{s.rcvr, argv, replyv})
	// The return value for the method is an error.
	errInter := returnValues[0].Interface()
	errmsg := ""
	if errInter != nil {
		errmsg = errInter.(error).Error()
	}
	free.sendResponse(sending, req, replyv.Interface(), codec, errmsg)
	free.freeRequest(req)
}
