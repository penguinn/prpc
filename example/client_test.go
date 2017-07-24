package example

import (
	"log"
	"net"
	"testing"
	"sync"
	"github.com/penguinn/prpc/example/proto"
	"github.com/penguinn/prpc"
	"github.com/penguinn/prpc/codec"
)

type Sjy struct {

}

func(p *Sjy) Add(args *proto.AddArgs, reply *proto.AddReply) error{
	reply.C = args.A + args.B
	return nil
}

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
