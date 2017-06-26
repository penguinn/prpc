// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package prpc

import (
	//"bufio"
	"errors"
	"io"
	"log"
	//"net"
	//"net/http"
	"sync"
	"reflect"
	"strings"
)

// ServerError represents an error that has been returned from
// the remote side of the RPC connection.
type ServerError string

func (e ServerError) Error() string {
	return string(e)
}

var ErrShutdown = errors.New("connection is shut down")

// Client represents an RPC Client.
// There may be multiple outstanding Calls associated
// with a single Client, and a Client may be used by
// multiple goroutines simultaneously.
type Client struct {
	mu					sync.RWMutex
	serviceMap 			map[string]*service

	codec 				Codec

	//reqMutex 			sync.Mutex // protects following
	//request  			Request
	hedMutex			sync.Mutex
	header 				Header

	mutex          		sync.Mutex // protects following
	seq            		uint64
	pending        		map[uint64]*Call
	closing        		bool // user has called Close
	shutdown       		bool // client has told us to stop
	CallbackMapLock 	sync.RWMutex
	CallbackMap   		map[string]func(*Client, Codec, Header) error

	free  				Free
}

// NewClient returns a new Client to handle requests to the
// set of services at the other end of the connection.
// It adds a buffer to the write side of the connection so
// the header and payload are sent as a unit.
//func NewClient(conn io.ReadWriteCloser) *Client {
//	encBuf := bufio.NewWriter(conn)
//	client := &gobClientCodec{conn, gob.NewDecoder(conn), gob.NewEncoder(encBuf), encBuf}
//	return NewClientWithCodec(client)
//}

// NewClientWithCodec is like NewClient but uses the specified
// codec to encode requests and decode responses.
func NewClientWithCodec(codec Codec) *Client {
	client := &Client{
		codec:   codec,
		pending: make(map[uint64]*Call),
		serviceMap:make(map[string]*service),
	}
	go client.ReadLoop()
	return client
}

func (client *Client) Register(rcvr interface{}) error {
	return client.register(rcvr, "", false)
}

// RegisterName is like Register but uses the provided name for the type
// instead of the receiver's concrete type.
func (client *Client) RegisterName(name string, rcvr interface{}) error {
	return client.register(rcvr, name, true)
}

func (client *Client) register(rcvr interface{}, name string, useName bool) error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.serviceMap == nil {
		client.serviceMap = make(map[string]*service)
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
	if _, present := client.serviceMap[sname]; present {
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
	client.serviceMap[s.name] = s
	return nil
}

// Call invokes the named function, waits for it to complete, and returns its error status.
func (client *Client) Call(serviceMethod string, args interface{}, reply interface{}) error {
	call := <-client.Go(serviceMethod, args, reply, make(chan *Call, 1)).Done
	return call.Error
}

// Go invokes the function asynchronously. It returns the Call structure representing
// the invocation. The done channel will signal when the call is complete by returning
// the same Call object. If done is nil, Go will allocate a new channel.
// If non-nil, done must be buffered or Go will deliberately crash.
func (client *Client) Go(serviceMethod string, args interface{}, reply interface{}, done chan *Call) *Call {
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
	client.send(call)
	return call
}

func (client *Client) send(call *Call) {
	client.hedMutex.Lock()
	defer client.hedMutex.Unlock()

	// Register this call.
	client.mutex.Lock()
	if client.shutdown || client.closing {
		call.Error = ErrShutdown
		client.mutex.Unlock()
		call.done()
		return
	}
	seq := client.seq
	client.seq++
	client.pending[seq] = call
	client.mutex.Unlock()

	// Encode and send the request.
	client.header.Seq = seq
	client.header.ServiceMethod = call.ServiceMethod
	err := client.codec.Write(&client.header, call.Args)
	if err != nil {
		client.mutex.Lock()
		call = client.pending[seq]
		delete(client.pending, seq)
		client.mutex.Unlock()
		if call != nil {
			call.Error = err
			call.done()
		}
	}
}

func (client *Client) ReadLoop() (err error){
	var header Header
	for err == nil {
		header = Header{}
		log.Println(1)
		err := client.codec.ReadHeader(&header)
		if err != nil {
			log.Println("[error] read header error:", err)
			break
		}
		if header.IsResponse {
			log.Println("Response")
			log.Println("response.ServiceMethod ", header.ServiceMethod)
			f, ok := client.CallbackMap[header.ServiceMethod]
			if ok {
				err = f(client, client.codec, header)
			}
			seq := header.Seq
			client.mutex.Lock()
			call := client.pending[seq]
			delete(client.pending, seq)
			client.mutex.Unlock()

			switch {
			case call == nil:
				// We've got no pending call. That usually means that
				// WriteRequest partially failed, and call was already
				// removed; response is a client telling us about an
				// error reading request body. We should still attempt
				// to read error body, but there's no one to give it to.
				err = client.codec.ReadBody(nil)
				if err != nil {
					err = errors.New("reading error body: " + err.Error())
				}
			case header.Error != "":
				// We've got an error response. Give this to the request;
				// any subsequent requests will get the ReadResponseBody
				// error if there is one.
				call.Error = ServerError(header.Error)
				err = client.codec.ReadBody(nil)
				if err != nil {
					err = errors.New("reading error body: " + err.Error())
				}
				call.done()
			default:
				err = client.codec.ReadBody(call.Reply)
				if err != nil {
					call.Error = errors.New("reading body " + err.Error())
				}
				call.done()
			}
		}else{
			log.Println("Request")
			sending := new(sync.Mutex)
			service, mtype, argv, replyv, keepReading, err := client.docodeReqHeader(&header)
			if err != nil {
				if !keepReading {
					return err
				}
				// send a response if we actually managed to read a header.
				if &header != nil {
					client.free.sendResponse(sending, &header, invalidRequest, client.codec, err.Error())
					client.free.freeRequest(&header)
				}
				return err
			}
			go service.dispose(client.free, sending, mtype, &header, argv, replyv, client.codec)
		}
	}
	// Terminate pending calls.
	client.hedMutex.Lock()
	//defer client.hedMutex.Unlock()
	client.mutex.Lock()
	//defer client.mutex.Unlock()
	client.shutdown = true
	closing := client.closing
	if err == io.EOF {
		if closing {
			err = ErrShutdown
		} else {
			err = io.ErrUnexpectedEOF
		}
	}
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
	client.mutex.Unlock()
	client.hedMutex.Unlock()
	if err != io.EOF && !closing {
		log.Println("rpc: client protocol error:", err)
	}
	return err
}

//func (client *Client) ReadRequest(codec Codec) (service *service, mtype *methodType, req *Header, argv, replyv reflect.Value, keepReading bool, err error) {
//	return client.readRequest(codec)
//}
func (client *Client) docodeReqHeader(header *Header) (service *service, mtype *methodType, argv, replyv reflect.Value, keepReading bool, err error) {
	service, mtype, keepReading, err = client.decodeRequestHeader(header)
	if err != nil {
		if !keepReading {
			return
		}
		// discard body
		client.codec.ReadBody(nil)
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
	if err = client.codec.ReadBody(argv.Interface()); err != nil {
		return
	}
	if argIsValue {
		argv = argv.Elem()
	}

	replyv = reflect.New(mtype.ReplyType.Elem())
	return
}

func (client *Client) decodeRequestHeader(req *Header) (service *service, mtype *methodType, keepReading bool, err error) {
	// Grab the request header.
	//req = client.free.getRequest()
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
	client.mu.RLock()
	service = client.serviceMap[serviceName]
	client.mu.RUnlock()
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

func (client *Client) Close() error {
	client.mutex.Lock()
	if client.closing {
		client.mutex.Unlock()
		return ErrShutdown
	}
	client.closing = true
	client.mutex.Unlock()
	return client.codec.Close()
}
