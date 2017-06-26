// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

/*
	Package rpc provides access to the exported methods of an object across a
	network or other I/O connection.  A server registers an object, making it visible
	as a service with the name of the type of the object.  After registration, exported
	methods of the object will be accessible remotely.  A server may register multiple
	objects (services) of different types but it is an error to register multiple
	objects of the same type.

	Only methods that satisfy these criteria will be made available for remote access;
	other methods will be ignored:

		- the method's type is exported.
		- the method is exported.
		- the method has two arguments, both exported (or builtin) types.
		- the method's second argument is a pointer.
		- the method has return type error.

	In effect, the method must look schematically like

		func (t *T) MethodName(argType T1, replyType *T2) error

	where T, T1 and T2 can be marshaled by encoding/gob.
	These requirements apply even if a different codec is used.
	(In the future, these requirements may soften for custom codecs.)

	The method's first argument represents the arguments provided by the caller; the
	second argument represents the result parameters to be returned to the caller.
	The method's return value, if non-nil, is passed back as a string that the client
	sees as if created by errors.New.  If an error is returned, the reply parameter
	will not be sent back to the client.

	The server may handle requests on a single connection by calling ServeConn.  More
	typically it will create a network listener and call Accept or, for an HTTP
	listener, HandleHTTP and http.Serve.

	A client wishing to use the service establishes a connection and then invokes
	NewClient on the connection.  The convenience function Dial (DialHTTP) performs
	both steps for a raw network connection (an HTTP connection).  The resulting
	Client object has two methods, Call and Go, that specify the service and method to
	call, a pointer containing the arguments, and a pointer to receive the result
	parameters.

	The Call method waits for the remote call to complete while the Go method
	launches the call asynchronously and signals completion using the Call
	structure's Done channel.

	Unless an explicit codec is set up, package encoding/gob is used to
	transport the data.

	Here is a simple example.  A server wishes to export an object of type Arith:

		package server

		type Args struct {
			A, B int
		}

		type Quotient struct {
			Quo, Rem int
		}

		type Arith int

		func (t *Arith) Multiply(args *Args, reply *int) error {
			*reply = args.A * args.B
			return nil
		}

		func (t *Arith) Divide(args *Args, quo *Quotient) error {
			if args.B == 0 {
				return errors.New("divide by zero")
			}
			quo.Quo = args.A / args.B
			quo.Rem = args.A % args.B
			return nil
		}

	The server calls (for HTTP service):

		arith := new(Arith)
		rpc.Register(arith)
		rpc.HandleHTTP()
		l, e := net.Listen("tcp", ":1234")
		if e != nil {
			log.Fatal("listen error:", e)
		}
		go http.Serve(l, nil)

	At this point, clients can see a service "Arith" with methods "Arith.Multiply" and
	"Arith.Divide".  To invoke one, a client first dials the server:

		client, err := rpc.DialHTTP("tcp", serverAddress + ":1234")
		if err != nil {
			log.Fatal("dialing:", err)
		}

	Then it can make a remote call:

		// Synchronous call
		args := &server.Args{7,8}
		var reply int
		err = client.Call("Arith.Multiply", args, &reply)
		if err != nil {
			log.Fatal("arith error:", err)
		}
		fmt.Printf("Arith: %d*%d=%d", args.A, args.B, reply)

	or

		// Asynchronous call
		quotient := new(Quotient)
		divCall := client.Go("Arith.Divide", args, quotient, nil)
		replyCall := <-divCall.Done	// will be equal to divCall
		// check errors, print, etc.

	A server implementation will often provide a simple, type-safe wrapper for the
	client.
*/
package prpc

import (
	//"bufio"
	"errors"
	"io"
	"log"
	//"net"
	"reflect"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	// Defaults used by HandleHTTP
	DefaultRPCPath   = "/_goRPC_"
	DefaultDebugPath = "/debug/rpc"
)

// Precompute the reflect type for error. Can't use error directly
// because Typeof takes an empty interface value. This is annoying.
var typeOfError = reflect.TypeOf((*error)(nil)).Elem()

var debugLog = false

type methodType struct {
	sync.Mutex // protects counters
	method     reflect.Method
	ArgType    reflect.Type
	ReplyType  reflect.Type
	numCalls   uint
}

type service struct {
	name   string                 // name of service
	rcvr   reflect.Value          // receiver of methods for the service
	typ    reflect.Type           // type of the receiver
	method map[string]*methodType // registered methods
}

type Free struct {
	reqLock           sync.Mutex // protects freeReq
	freeReq           *Header
	respLock          sync.Mutex // protects freeResp
	freeResp          *Header
}

// Server represents an RPC Server.
type Server struct {
	//codec             	Codec
	mu                	sync.RWMutex // protects the serviceMap
	serviceMap        	map[string]*service
	hedMutex			sync.Mutex
	header 				Header

	mutex          		sync.Mutex // protects following
	seq            		uint64
	pending        		map[uint64]*Call
	closing        		bool // user has called Close
	shutdown       		bool // server has told us to stop

	free 				Free
}

// NewServer returns a new Server.
func NewServer() *Server {
	return &Server{serviceMap: make(map[string]*service), pending:make(map[uint64]*Call)}
}

//func NewServerWithCodec(codec Codec) *Server{
//	return &Server{serviceMap:make(map[string]*service), codec:codec}
//}

// DefaultServer is the default instance of *Server.
var DefaultServer = NewServer()

// Is this an exported - upper case - name?
func isExported(name string) bool {
	rune, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(rune)
}

// Is this type exported or a builtin?
func isExportedOrBuiltinType(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	// PkgPath will be non-empty even for an exported type,
	// so we need to check the type name as well.
	return isExported(t.Name()) || t.PkgPath() == ""
}

// Register publishes in the server the set of methods of the
// receiver value that satisfy the following conditions:
//	- exported method of exported type
//	- two arguments, both of exported type
//	- the second argument is a pointer
//	- one return value, of type error
// It returns an error if the receiver is not an exported type or has
// no suitable methods. It also logs the error using package log.
// The client accesses each method using a string of the form "Type.Method",
// where Type is the receiver's concrete type.
func (server *Server) Register(rcvr interface{}) error {
	return server.register(rcvr, "", false)
}

// RegisterName is like Register but uses the provided name for the type
// instead of the receiver's concrete type.
func (server *Server) RegisterName(name string, rcvr interface{}) error {
	return server.register(rcvr, name, true)
}

func (server *Server) register(rcvr interface{}, name string, useName bool) error {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.serviceMap == nil {
		server.serviceMap = make(map[string]*service)
	}
	s := new(service)
	s.typ = reflect.TypeOf(rcvr)
	s.rcvr = reflect.ValueOf(rcvr)
	sname := reflect.Indirect(s.rcvr).Type().Name()
	if useName {
		sname = name
	}
	if sname == "" {
		s := "rpc.Register: no service name for type " + s.typ.String()
		log.Print(s)
		return errors.New(s)
	}
	if !isExported(sname) && !useName {
		s := "rpc.Register: type " + sname + " is not exported"
		log.Print(s)
		return errors.New(s)
	}
	if _, present := server.serviceMap[sname]; present {
		return errors.New("rpc: service already defined: " + sname)
	}
	s.name = sname

	// Install the methods
	s.method = suitableMethods(s.typ, true)

	if len(s.method) == 0 {
		str := ""

		// To help the user, see if a pointer receiver would work.
		method := suitableMethods(reflect.PtrTo(s.typ), false)
		if len(method) != 0 {
			str = "rpc.Register: type " + sname + " has no exported methods of suitable type (hint: pass a pointer to value of that type)"
		} else {
			str = "rpc.Register: type " + sname + " has no exported methods of suitable type"
		}
		log.Print(str)
		return errors.New(str)
	}
	server.serviceMap[s.name] = s
	return nil
}

// suitableMethods returns suitable Rpc methods of typ, it will report
// error using log if reportErr is true.
func suitableMethods(typ reflect.Type, reportErr bool) map[string]*methodType {
	methods := make(map[string]*methodType)
	for m := 0; m < typ.NumMethod(); m++ {
		method := typ.Method(m)
		mtype := method.Type
		mname := method.Name
		// Method must be exported.
		if method.PkgPath != "" {
			continue
		}
		// Method needs three ins: receiver, *args, *reply.
		if mtype.NumIn() != 3 {
			if reportErr {
				log.Println("method", mname, "has wrong number of ins:", mtype.NumIn())
			}
			continue
		}
		// First arg need not be a pointer.
		argType := mtype.In(1)
		if !isExportedOrBuiltinType(argType) {
			if reportErr {
				log.Println(mname, "argument type not exported:", argType)
			}
			continue
		}
		// Second arg must be a pointer.
		replyType := mtype.In(2)
		if replyType.Kind() != reflect.Ptr {
			if reportErr {
				log.Println("method", mname, "reply type not a pointer:", replyType)
			}
			continue
		}
		// Reply type must be exported.
		if !isExportedOrBuiltinType(replyType) {
			if reportErr {
				log.Println("method", mname, "reply type not exported:", replyType)
			}
			continue
		}
		// Method needs one out.
		if mtype.NumOut() != 1 {
			if reportErr {
				log.Println("method", mname, "has wrong number of outs:", mtype.NumOut())
			}
			continue
		}
		// The return type of the method must be error.
		if returnType := mtype.Out(0); returnType != typeOfError {
			if reportErr {
				log.Println("method", mname, "returns", returnType.String(), "not error")
			}
			continue
		}
		methods[mname] = &methodType{method: method, ArgType: argType, ReplyType: replyType}
	}
	return methods
}

// A value sent as a placeholder for the server's response value when the server
// receives an invalid request. It is never decoded by the client since the Response
// contains an error when it is used.
var invalidRequest = struct{}{}

func (m *methodType) NumCalls() (n uint) {
	m.Lock()
	n = m.numCalls
	m.Unlock()
	return n
}

func (s *service) Dispose(server *Server, sending *sync.Mutex, mtype *methodType, req *Header, argv, replyv reflect.Value, codec Codec) {
	s.dispose(server.free, sending, mtype, req, argv, replyv, codec)
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

func (server *Server) Go(serviceMethod string, codec Codec, args interface{}, reply interface{}, done chan *Call) *Call {
	call := new(Call)
	call.ServiceMethod = serviceMethod
	call.Args = args
	call.Reply = reply
	if done == nil {
		done = make(chan *Call, 10) // buffered.
	} else {
		// If caller passes done != nil, it must arrange that
		// done has enough buffer for the number of simultaneous
		// RPCs that will be using that channel. If the channel
		// is totally unbuffered, it's best not to run at all.
		if cap(done) == 0 {
			log.Panic("rpc: done channel is unbuffered")
		}
	}
	call.Done = done
	server.send(call, codec)
	return call
}

// Call invokes the named function, waits for it to complete, and returns its error status.
func (server *Server) Call(serviceMethod string, codec Codec, args interface{}, reply interface{}) error {
	call := <-server.Go(serviceMethod, codec, args, reply, make(chan *Call, 1)).Done
	return call.Error
}

func (server *Server) send(call *Call, codec Codec) {
	server.hedMutex.Lock()
	defer server.hedMutex.Unlock()

	// Register this call.
	server.mutex.Lock()
	if server.shutdown || server.closing {
		call.Error = ErrShutdown
		server.mutex.Unlock()
		call.done()
		return
	}
	seq := server.seq
	server.seq++
	server.pending[seq] = call
	server.mutex.Unlock()

	// Encode and send the request.
	server.header.Seq = seq
	server.header.ServiceMethod = call.ServiceMethod
	server.header.IsResponse = false
	err := codec.Write(&server.header, call.Args)
	if err != nil {
		server.mutex.Lock()
		call = server.pending[seq]
		delete(server.pending, seq)
		server.mutex.Unlock()
		if call != nil {
			call.Error = err
			call.done()
		}
	}
}

func (free *Free) getRequest() *Header {
	free.reqLock.Lock()
	defer free.reqLock.Unlock()
	req := free.freeReq
	if req == nil {
		req = new(Header)
	} else {
		free.freeReq = req.next
		*req = Header{}
	}
	return req
}

func (free *Free) FreeRequest(req *Header) {
	free.freeRequest(req)
}

func (free *Free) freeRequest(req *Header) {
	free.reqLock.Lock()
	defer free.reqLock.Unlock()
	req.next = free.freeReq
	free.freeReq = req
}

func (free *Free) getResponse() *Header {
	free.respLock.Lock()
	defer free.respLock.Unlock()
	resp := free.freeResp
	if resp == nil {
		resp = new(Header)
	} else {
		free.freeResp = resp.next
		*resp = Header{}
	}
	return resp
}

func (free *Free) freeResponse(resp *Header) {
	free.respLock.Lock()
	defer free.respLock.Unlock()
	resp.next = free.freeResp
	free.freeResp = resp
}

func (free *Free) SendResponse(sending *sync.Mutex, req *Header, reply interface{}, codec Codec, errmsg string) {
	free.sendResponse(sending, req, reply, codec, errmsg)
}
func (free *Free) sendResponse(sending *sync.Mutex, req *Header, reply interface{}, codec Codec, errmsg string) {
	resp := free.getResponse()
	// Encode the response header
	resp.ServiceMethod = req.ServiceMethod
	if errmsg != "" {
		resp.Error = errmsg
		reply = invalidRequest
	}
	resp.Seq = req.Seq
	resp.IsResponse = true
	sending.Lock()
	err := codec.Write(resp, reply)
	if debugLog && err != nil {
		log.Println("rpc: writing response:", err)
	}
	sending.Unlock()
	free.freeResponse(resp)
}

//func (server *Server) ReadRequest(codec Codec) (service *service, mtype *methodType, req *Header, argv, replyv reflect.Value, keepReading bool, err error) {
//	return server.readRequest(codec)
//}
func (server *Server) decodeReqHeader(header *Header, codec Codec) (service *service, mtype *methodType, argv, replyv reflect.Value, keepReading bool, err error) {
	service, mtype, keepReading, err = server.decodeRequestHeader(header)
	if err != nil {
		if !keepReading {
			return
		}
		// discard body
		codec.ReadBody(nil)
		return
	}

	// Decode the argument value.
	argIsValue := false // if true, need to indirect before calling.
	if mtype.ArgType.Kind() == reflect.Ptr {
		argv = reflect.New(mtype.ArgType.Elem())
	} else {
		argv = reflect.New(mtype.ArgType)
		argIsValue = true
	}
	// argv guaranteed to be a pointer now.
	if err = codec.ReadBody(argv.Interface()); err != nil {
		return
	}
	if argIsValue {
		argv = argv.Elem()
	}

	replyv = reflect.New(mtype.ReplyType.Elem())
	return
}

func (server *Server) decodeRequestHeader(req *Header) (service *service, mtype *methodType, keepReading bool, err error) {
	// Grab the request header.
	//req = server.free.getRequest()
	//
	//req = header

	// We read the header successfully. If we see an error now,
	// we can still recover and move on to the next request.
	keepReading = true

	dot := strings.LastIndex(req.ServiceMethod, ".")
	if dot < 0 {
		err = errors.New("rpc: service/method request ill-formed: " + req.ServiceMethod)
		return
	}
	serviceName := req.ServiceMethod[:dot]
	methodName := req.ServiceMethod[dot+1:]

	// Look up the request.
	server.mu.RLock()
	service = server.serviceMap[serviceName]
	server.mu.RUnlock()
	if service == nil {
		err = errors.New("rpc: can't find service " + req.ServiceMethod)
		return
	}
	mtype = service.method[methodName]
	if mtype == nil {
		err = errors.New("rpc: can't find method " + req.ServiceMethod)
	}
	return
}

func(server *Server) ReadLoop(codec Codec)(err error){
	var header Header
	for err == nil {
		header = Header{}
		err := codec.ReadHeader(&header)
		if err != nil {
			log.Println("[error] read header error:", err)
			break
		}
		if header.IsResponse {
			log.Println("Response")
			log.Println("response.ServiceMethod ", header.ServiceMethod)
			seq := header.Seq
			server.mutex.Lock()
			call := server.pending[seq]
			delete(server.pending, seq)
			server.mutex.Unlock()

			switch {
			case call == nil:
				// We've got no pending call. That usually means that
				// WriteRequest partially failed, and call was already
				// removed; response is a server telling us about an
				// error reading request body. We should still attempt
				// to read error body, but there's no one to give it to.
				err = codec.ReadBody(nil)
				if err != nil {
					err = errors.New("reading error body: " + err.Error())
				}
			case header.Error != "":
				// We've got an error response. Give this to the request;
				// any subsequent requests will get the ReadResponseBody
				// error if there is one.
				call.Error = ServerError(header.Error)
				err = codec.ReadBody(nil)
				if err != nil {
					err = errors.New("reading error body: " + err.Error())
				}
				call.done()
			default:
				err = codec.ReadBody(call.Reply)
				if err != nil {
					call.Error = errors.New("reading body " + err.Error())
				}
				call.done()
			}
		} else {
			log.Println("Request")
			sending := new(sync.Mutex)
			service, mtype, argv, replyv, keepReading, err := server.decodeReqHeader(&header, codec)
			log.Println(3)
			if err != nil {
				if !keepReading {
					log.Println(1)
					return err
				}
				// send a response if we actually managed to read a header.
				if &header != nil {
					server.free.sendResponse(sending, &header, invalidRequest, codec, err.Error())
					server.free.freeRequest(&header)
				}
				log.Println(2)
				return err
			}
			log.Println(4)
			go service.dispose(server.free, sending, mtype, &header, argv, replyv, codec)
		}
	}
	server.hedMutex.Lock()
	//defer client.hedMutex.Unlock()
	server.mutex.Lock()
	//defer client.mutex.Unlock()
	server.shutdown = true
	closing := server.closing
	if err == io.EOF {
		if closing {
			err = ErrShutdown
		} else {
			err = io.ErrUnexpectedEOF
		}
	}
	for _, call := range server.pending {
		call.Error = err
		call.done()
	}
	server.mutex.Unlock()
	server.hedMutex.Unlock()
	if err != io.EOF && !closing {
		log.Println("rpc: server protocol error:", err)
	}
	return nil
}