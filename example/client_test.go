package example

import (
	"log"
	"net"

	"github.com/penguinn/prpc/example/proto"

	"github.com/penguinn/prpc"

	"github.com/penguinn/prpc/codec"
	"testing"
	"sync"
)

type Sjy struct {

}

func(p *Sjy) Add(args *proto.AddArgs, reply *proto.AddReply) error{
	reply.C = args.A + args.B
	return nil
}

//type TestClient struct {
//	client *rpc.Client
//	addr   string
//}
//
//func NewClient(addr string) (*TestClient, error) {
//	conn, err := net.Dial("tcp", addr)
//	if err != nil {
//		return nil, err
//	}
//	codecTemp := codec.NewCodec(conn)
//	client := rpc.NewClientWithCodec(codecTemp)
//	client.CallbackMap = make(map[string]func(*rpc.Client, rpc.Codec, rpc.Header) error)
//	//client.CallbackFunc = func(client *rpc.Client, codec_ rpc.ClientCodec, response rpc.Response) error {
//	//	reply := &empty.Empty{}
//	//	codec_.ReadResponseBody(reply)
//	//	log.Println(response)
//	//	return nil
//	//}
//	//client.CallbackPrefix = "Callback"
//	return &TestClient{
//		client: client,
//		addr:   addr,
//	}, nil
//}
//func (this *TestClient) RegisterCallback(CallbackName string, f func(*rpc.Client, rpc.Codec, rpc.Header) error) {
//	this.client.CallbackMapLock.Lock()
//	defer this.client.CallbackMapLock.Unlock()
//	this.client.CallbackMap[CallbackName] = f
//}
//
//func (this *TestClient) RegisterName(name string, rcvr interface{}) error {
//	return this.client.RegisterName(name, rcvr)
//}
//
//func (this *TestClient) Call(serviceMethod string, args interface{}, reply interface{}) error {
//	return this.client.Call(serviceMethod, args, reply)
//}
//
//func CallbackPrint(client *rpc.Client, codecTemp rpc.Codec, response rpc.Header) error {
//	reply := &empty.Empty{}
//	codecTemp.ReadBody(reply)
//	log.Println(response)
//	return nil
//}

func Test_Client(t *testing.T) {
	var wg sync.WaitGroup
	conn, err := net.Dial("tcp", ":8081")
	if err != nil{
		log.Println("[error] dial tcp error:", err)
	}
	codecTemp := codec.NewCodec(conn)
	client := prpc.NewClientWithCodec(codecTemp)
	client.RegisterName("Sjy", new(Sjy))
	args := new(proto.ProtoArgs)
	args.A = 5
	args.B = 6
	reply := new(proto.ProtoReply)
	err = client.Call("Front.Mul", args, reply)
	if err != nil {
		log.Println(err)
	}
	log.Println("######",reply)
	wg.Add(1)
	wg.Wait()
}
