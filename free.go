package prpc

import (
	"sync"
	"log"
)

type Free struct {
	reqLock           sync.Mutex // protects freeReq
	freeReq           *Header
	respLock          sync.Mutex // protects freeResp
	freeResp          *Header
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