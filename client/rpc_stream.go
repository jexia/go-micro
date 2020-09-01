package client

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/micro/go-micro/v2/codec"
)

// Implements the streamer interface
type rpcStream struct {
	sync.RWMutex
	id       string
	closed   chan bool
	err      error
	request  Request
	response Response
	codec    codec.Codec
	context  context.Context

	// signal whether we should send EOS
	sendEOS bool

	// release releases the connection back to the pool
	release func(err error)
}

func (r *rpcStream) isClosed() bool {
	select {
	case <-r.closed:
		return true
	default:
		return false
	}
}

func (r *rpcStream) Context() context.Context {
	return r.context
}

func (r *rpcStream) Request() Request {
	return r.request
}

func (r *rpcStream) Response() Response {
	return r.response
}

func (r *rpcStream) Send(msg interface{}) error {
	r.Lock()
	defer r.Unlock()

	if r.isClosed() {
		r.err = errShutdown
		fmt.Printf("===> RPC CLIENT STREAM SEND ERROR SHUTDOWN\n")
		return errShutdown
	}

	req := codec.Message{
		Id:       r.id,
		Target:   r.request.Service(),
		Method:   r.request.Method(),
		Endpoint: r.request.Endpoint(),
		Type:     codec.Request,
	}

	if err := r.codec.Write(&req, msg); err != nil {
		r.err = err
		fmt.Printf("===> RPC CLIENT STREAM WRITE ERROR\n")
		return err
	}

	fmt.Printf("===> RPC CLIENT STREAM SENT (%s): %q %q\n", r.codec.String(), req, msg)

	return nil
}

func (r *rpcStream) Recv(msg interface{}) error {
	r.Lock()
	defer r.Unlock()

	if r.isClosed() {
		r.err = errShutdown
		fmt.Printf("===> RPC CLIENT STREAM RECV ERROR SHUTDOWN\n")
		return errShutdown
	}

	var resp codec.Message

	r.Unlock()
	err := r.codec.ReadHeader(&resp, codec.Response)
	r.Lock()
	fmt.Printf("===> RPC CLIENT STREAM RECV: %q\n", resp)
	if err != nil {
		if err == io.EOF && !r.isClosed() {
			r.err = io.ErrUnexpectedEOF
			return io.ErrUnexpectedEOF
		}
		r.err = err
		fmt.Printf("===> RPC CLIENT STREAM RECV GOT EOF ERROR FROM HEADER\n")
		return err
	}

	switch {
	case len(resp.Error) > 0:
		// We've got an error response. Give this to the request;
		// any subsequent requests will get the ReadResponseBody
		// error if there is one.
		if resp.Error != lastStreamResponseError {
			r.err = serverError(resp.Error)
			fmt.Printf("===> RPC CLIENT STREAM RECV SERVER ERROR: %q\n", r.err)
		} else {
			r.err = io.EOF
			fmt.Printf("===> RPC CLIENT STREAM RECV SET EOF\n")
		}
		r.Unlock()
		err = r.codec.ReadBody(nil)
		r.Lock()
		if err != nil {
			r.err = err
			fmt.Printf("===> RPC CLIENT STREAM RECV READ BODY ERROR 1: %q\n", err)
		}
	default:
		r.Unlock()
		err = r.codec.ReadBody(msg)
		r.Lock()
		if err != nil {
			r.err = err
			fmt.Printf("===> RPC CLIENT STREAM RECV READ BODY ERROR 2: %q\n", err)
		}
	}
	fmt.Printf("===> RPC CLIENT STREAM RECV STREAM MESSAGE (%s): %+v %+v\n", r.codec.String(), r, msg)

	return r.err
}

func (r *rpcStream) Error() error {
	r.RLock()
	defer r.RUnlock()
	return r.err
}

func (r *rpcStream) Close() error {
	r.Lock()
	fmt.Printf("===> RPC CLIENT STREAM CLOSE\n")
	select {
	case <-r.closed:
		r.Unlock()
		return nil
	default:
		close(r.closed)
		r.Unlock()

		// send the end of stream message
		if r.sendEOS {
			fmt.Printf("===> RPC CLIENT STREAM WRITE EOS\n")
			// no need to check for error
			r.codec.Write(&codec.Message{
				Id:       r.id,
				Target:   r.request.Service(),
				Method:   r.request.Method(),
				Endpoint: r.request.Endpoint(),
				Type:     codec.Error,
				Error:    lastStreamResponseError,
			}, nil)
		}

		err := r.codec.Close()

		// release the connection
		r.release(r.Error())

		// return the codec error
		return err
	}
}
