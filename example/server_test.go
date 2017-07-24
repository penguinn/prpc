package example

import (
	"log"
	"time"
	"net"
	"testing"
	"github.com/penguinn/prpc/example/proto"
	"github.com/penguinn/prpc"
	"github.com/penguinn/prpc/codec"
)

type Front struct {
}

func (t *Front) Mul(args *proto.ProtoArgs, reply *proto.ProtoReply) error {
	reply.C = args.A * args.B
	return nil
}

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
	log.Println(1111)
}
