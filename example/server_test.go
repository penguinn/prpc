package example

import (
	"log"
	"github.com/penguinn/prpc/example/proto"

	"github.com/penguinn/prpc"

	"github.com/golang/protobuf/ptypes/empty"
	"testing"
	"net"
	"github.com/penguinn/prpc/codec"
	"time"
)

type Front struct {
}

//func (t *Front) Auth(args *proto.AuthArgs, reply *proto.AuthReply) error {
//	if args.User == "puper" {
//		reply.Success = true
//	} else {
//		reply.Success = false
//	}
//	return nil
//}

func (t *Front) Mul(args *proto.ProtoArgs, reply *proto.ProtoReply) error {
	reply.C = args.A * args.B
	return nil
}

var invalidRequest = &empty.Empty{}

//type TestServer struct {
//	addr     string
//	server   *rpc.Server
//	listener net.Listener
//}

//func NewTestServer(addr string) *TestServer {
//
//	return &TestServer{
//		addr:   addr,
//		server: rpc.NewServer(),
//	}
//}
//
//func (this *TestServer) Start() {
//	var (
//		err error
//	)
//	this.listener, err = net.Listen("tcp", this.addr)
//	if err != nil {
//		panic(err)
//	}
//	for {
//		conn, err := this.listener.Accept()
//		if err != nil {
//			log.Print("rpc.Serve: accept:", err.Error())
//			return
//		}
//		go this.ServeConn(conn)
//	}
//}
//
//func (this *TestServer) ServeConn(conn io.ReadWriteCloser) {
//	srv := codec.NewCodec(conn)
//	this.ServeCodec(srv)
//}
//
//func (this *TestServer) ServeCodec(code rpc.Codec) {
//	var (
//		err error
//	)
//	if err = this.Auth(code); err != nil {
//		code.Close()
//		log.Println(err)
//		return
//	}
//	sending := new(sync.Mutex)
//	for {
//		service, mtype, req, argv, replyv, keepReading, err := this.server.ReadRequest(code)
//		if err != nil {
//			if !keepReading {
//				break
//			}
//			// send a response if we actually managed to read a header.
//			if req != nil {
//				this.server.SendResponse(sending, req, invalidRequest, code, err.Error())
//				this.server.FreeRequest(req)
//			}
//			continue
//		}
//		go service.Call(this.server, sending, mtype, req, argv, replyv, code)
//	}
//	code.Close()
//}
//
//func (this *TestServer) Auth(code rpc.Codec) error {
//	sending := new(sync.Mutex)
//	service, mtype, req, argv, replyv, keepReading, err := this.server.ReadRequest(code)
//	if err != nil {
//		if !keepReading {
//			return err
//		}
//		// send a response if we actually managed to read a header.
//		if req != nil {
//			this.server.SendResponse(sending, req, invalidRequest, code, err.Error())
//			this.server.FreeRequest(req)
//		}
//		return err
//	}
//	if req.ServiceMethod != "Front.Auth" {
//		this.server.SendResponse(sending, req, invalidRequest, code, "")
//		this.server.FreeRequest(req)
//		return errors.New("not auth service")
//	}
//	service.Call(this.server, sending, mtype, req, argv, replyv, code)
//	fmt.Println(replyv)
//	reply := replyv.Interface().(*proto.AuthReply)
//	fmt.Println(reply)
//	if !reply.Success {
//		log.Println(9)
//		err = code.Write(&rpc.Header{
//			ServiceMethod: "Callback.Test",
//			IsResponse:true,
//		}, invalidRequest)
//		log.Println(err)
//		return errors.New("auth failed")
//	}
//	return nil
//}
//
//func (this *TestServer) RegisterName(name string, rcvr interface{}) error {
//	return this.server.RegisterName(name, rcvr)
//}

func Test_Server(t *testing.T) {
	server := prpc.NewServer()
	server.RegisterName("Front", new(Front))
	listener, err := net.Listen("tcp", ":8081")
	if err != nil{
		log.Println("[error] Listen tcp error:", err)
	}
	for{
		conn, err := listener.Accept()
		if err != nil{
			log.Println("[error] Accept tcp error:", err)
		}
		codecTemp := codec.NewCodec(conn)
		go server.ReadLoop(codecTemp)

		time.Sleep(time.Second*2)
		args := new(proto.AddArgs)
		args.A = 7
		args.B = 9
		reply := new(proto.AddReply)
		server.Call("Sjy.Add",codecTemp, args, reply)
		log.Println("##########",reply)
	}
	//server.Start(":8081")
	log.Println(1111)
}
