package prpc

import (
	"sync"
	"reflect"
	"log"
	"errors"
	"io"
	"strings"
)

type methodType struct {
	sync.Mutex // protects counters
	method     reflect.Method
	ArgType    reflect.Type
	ReplyType  reflect.Type
	numCalls   uint
}

type Common struct {
	mu					sync.RWMutex
	serviceMap 			map[string]*service

	hedMutex				sync.Mutex
	header 				Header

	mutex          		sync.Mutex // protects following
	seq            		uint64
	pending        		map[uint64]*Call
	closing        		bool // user has called Close
	shutdown       		bool // server has told us to stop

	Free
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
func (common *Common) Register(rcvr interface{}) error {
	return common.register(rcvr, "", false)
}

// RegisterName is like Register but uses the provided name for the type
// instead of the receiver's concrete type.
func (common *Common) RegisterName(name string, rcvr interface{}) error {
	return common.register(rcvr, name, true)
}

func (common *Common) register(rcvr interface{}, name string, useName bool) error {
	common.mu.Lock()
	defer common.mu.Unlock()
	if common.serviceMap == nil {
		common.serviceMap = make(map[string]*service)
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
	if _, present := common.serviceMap[sname]; present {
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
	common.serviceMap[s.name] = s
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

func(common *Common) ReadLoop(codec Codec)(err error){
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
			common.mutex.Lock()
			call := common.pending[seq]
			delete(common.pending, seq)
			common.mutex.Unlock()

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
			service, mtype, argv, replyv, keepReading, err := common.decodeReqHeader(&header, codec)
			log.Println(3)
			if err != nil {
				if !keepReading {
					log.Println(1)
					return err
				}
				// send a response if we actually managed to read a header.
				if &header != nil {
					common.sendResponse(sending, &header, invalidRequest, codec, err.Error())
					common.freeRequest(&header)
				}
				return err
			}
			go service.dispose(common.Free, sending, mtype, &header, argv, replyv, codec)
		}
	}
	common.hedMutex.Lock()
	//defer client.hedMutex.Unlock()
	common.mutex.Lock()
	//defer client.mutex.Unlock()
	common.shutdown = true
	closing := common.closing
	if err == io.EOF {
		if closing {
			err = ErrShutdown
		} else {
			err = io.ErrUnexpectedEOF
		}
	}
	for _, call := range common.pending {
		call.Error = err
		call.done()
	}
	common.mutex.Unlock()
	common.hedMutex.Unlock()
	if err != io.EOF && !closing {
		log.Println("rpc: server protocol error:", err)
	}
	return nil
}

func (common *Common) decodeReqHeader(header *Header, codec Codec) (service *service, mtype *methodType, argv, replyv reflect.Value, keepReading bool, err error) {
	service, mtype, keepReading, err = common.decodeRequestHeader(header)
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

func (common *Common) decodeRequestHeader(req *Header) (service *service, mtype *methodType, keepReading bool, err error) {
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
	common.mu.RLock()
	service = common.serviceMap[serviceName]
	common.mu.RUnlock()
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