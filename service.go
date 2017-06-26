package prpc

import "sync"

type Service struct {
	serviceRLock		sync.RWMutex
	serviceMap 			map[string]*service
	codec 				Codec

}

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
