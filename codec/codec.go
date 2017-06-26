package codec

import (
	"bufio"
	"fmt"
	"io"
	"sync"

	"github.com/penguinn/prpc"

	"github.com/golang/protobuf/proto"
	"github.com/penguinn/prpc/codec/wirepb"
)
const defaultBufferSize = 4 * 1024

type serverCodec struct {
	mu   sync.Mutex // exclusive writer lock
	resp wirepb.ResponseHeader
	enc  *Encoder
	w    *bufio.Writer

	req wirepb.RequestHeader
	dec *Decoder
	c   io.Closer
}

type codec struct{
	mu   sync.Mutex // exclusive writer lock
	resp wirepb.ResponseHeader
	enc  *Encoder
	w    *bufio.Writer

	req wirepb.RequestHeader
	dec *Decoder
	c   io.Closer
}

func NewCodec(rwc io.ReadWriteCloser) prpc.Codec {
	w := bufio.NewWriterSize(rwc, defaultBufferSize)
	r := bufio.NewReaderSize(rwc, defaultBufferSize)
	return &codec{
		enc: NewEncoder(w),
		w:   w,
		dec: NewDecoder(r),
		c:   rwc,
	}
}

func(c *codec) Write(header *prpc.Header, body interface{}) error{
	c.mu.Lock()
	defer c.mu.Unlock()
	if header.IsResponse{
		c.resp.Method = header.ServiceMethod
		c.resp.Seq = header.Seq
		c.resp.Error = header.Error
		c.resp.IsResponse = header.IsResponse

		err := encode(c.enc, &c.resp)
		if err != nil {
			return err
		}
		if err = encode(c.enc, body); err != nil {
			return err
		}
		err = c.w.Flush()
		return err
	}else {
		c.req.Method = header.ServiceMethod
		c.req.Seq = header.Seq
		c.req.IsResponse = header.IsResponse

		err := encode(c.enc, &c.req)
		if err != nil {
			return err
		}
		if err = encode(c.enc, body); err != nil {
			return err
		}
		err = c.w.Flush()
		return err
	}
}

func(c *codec) ReadHeader(header *prpc.Header) (error){
	c.resp.Reset()
	if err := c.dec.Decode(&c.resp); err != nil {
		return err
	}

	header.ServiceMethod = c.resp.Method
	header.Seq = c.resp.Seq
	header.Error = c.resp.Error
	header.IsResponse = c.resp.IsResponse
	return nil
}

func(c *codec) ReadBody(body interface{}) error{
	if pb, ok := body.(proto.Message); ok {
		return c.dec.Decode(pb)
	}
	return fmt.Errorf("%T does not implement proto.Message", body)
}

func(c *codec) Close() error{
	return c.c.Close()
}

func encode(enc *Encoder, m interface{}) (err error) {
	if pb, ok := m.(proto.Message); ok {
		return enc.Encode(pb)
	}
	return fmt.Errorf("%T does not implement proto.Message", m)
}