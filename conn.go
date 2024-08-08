/**
 * Golang socket.io
 * Copyright (C) 2024 Kevin Z <zyxkad@gmail.com>
 * All rights reserved
 *
 *  This program is free software: you can redistribute it and/or modify
 *  it under the terms of the GNU Affero General Public License as published
 *  by the Free Software Foundation, either version 3 of the License, or
 *  (at your option) any later version.
 *
 *  This program is distributed in the hope that it will be useful,
 *  but WITHOUT ANY WARRANTY; without even the implied warranty of
 *  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *  GNU Affero General Public License for more details.
 *
 *  You should have received a copy of the GNU Affero General Public License
 *  along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package socket

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"

	"github.com/LiterMC/socket.io/engine.io"
	"github.com/LiterMC/socket.io/internal/utils"
)

var (
	errMultipleOpen = errors.New("Socket.IO: socket was already opened")
	errNotString    = errors.New("Socket.IO: the first argument must be a event name string")
	errRecvText     = errors.New("Socket.IO: got plaintext data when reconstructing a packet")
	errRecvByte     = errors.New("Socket.IO: got binary data when not reconstructing a packet")
)

type ConnectError struct {
	Reason string
}

var _ error = (*ConnectError)(nil)

func (e *ConnectError) Error() string {
	return "Socket.IO: connect error: " + e.Reason
}

type SocketStatus = int32

const (
	SocketClosed SocketStatus = iota
	SocketOpening
	SocketConnected
)

type Socket struct {
	io *engine.Socket

	mux           sync.RWMutex
	status        atomic.Int32
	sid, pid      string
	namespace     string
	autoReconnect bool
	auth          map[string]any
	lastOffset    string

	packet               Packet
	reconstructingAttach int

	ackMux  sync.Mutex
	ackId   int
	ackChan map[int]chan []any

	connectHandles       utils.HandlerList[*Socket, string]
	disconnectHandles    utils.HandlerList[*Socket, string]
	beforeConnectHandles utils.HandlerList[*Socket, struct{}]
	errorHandles         utils.HandlerList[*Socket, error]
	packetHandlers       utils.HandlerList[*Socket, *Packet]
	messageHandlers      utils.HandlerList[string, []any]

	msgbuf [][]byte
}

type Option = func(*Socket)

func WithAuth(auth map[string]any) Option {
	return func(s *Socket) {
		s.auth = auth
	}
}

func WithAuthToken(token string) Option {
	return func(s *Socket) {
		s.auth = map[string]any{
			"token": token,
		}
	}
}

func WithAuthTokenFn(tokenGen func() (string, error)) Option {
	return func(s *Socket) {
		s.auth = map[string]any{
			"token": tokenGen,
		}
	}
}

func NewSocket(io *engine.Socket, options ...Option) (s *Socket) {
	s = &Socket{
		io: io,

		ackChan: make(map[int]chan []any),
	}

	for _, opt := range options {
		opt(s)
	}

	io.OnConnect(func(*engine.Socket) {
		s.mux.RLock()
		reconnect := s.autoReconnect
		s.mux.RUnlock()
		if reconnect {
			if s.status.CompareAndSwap(SocketClosed, SocketOpening) {
				if err := s.sendConnPkt(); err != nil {
					s.onError(err)
				}
			}
		}
	})
	io.OnDisconnect(func(_ *engine.Socket, err error) {
		s.disconnected()
		if err != nil {
			s.onError(err)
		}
	})
	io.OnMessage(s.onMessage)
	return
}

func (s *Socket) ID() string {
	s.mux.RLock()
	defer s.mux.RUnlock()
	return s.sid
}

func (s *Socket) Status() SocketStatus {
	return s.status.Load()
}

func (s *Socket) IO() *engine.Socket {
	return s.io
}

var (
	typSocket = reflect.TypeOf((*Socket)(nil))
	typError  = reflect.TypeOf((*error)(nil)).Elem()
)

func (s *Socket) sendConnPkt() error {
	s.beforeConnectHandles.Call(s, struct{}{})
	pkt := &Packet{
		typ:       CONNECT,
		namespace: s.namespace,
	}
	data := make(map[string]any, 4)
	if s.auth != nil {
		for k, v0 := range s.auth {
			if m, ok := v0.(json.Marshaler); ok {
				data[k] = m
				continue
			}
			v := reflect.ValueOf(v0)
			if t := v.Type(); t.Kind() == reflect.Func {
				var in []reflect.Value
				if t.NumIn() > 1 {
					return errors.New("socket.io: \"" + k + "\": The callback function can only have maximum one input argument")
				}
				if t.NumIn() == 1 {
					if t.In(0) != typSocket {
						return errors.New("socket.io: \"" + k + "\": The first input of the callback function must be *Socket")
					}
					in = append(in, reflect.ValueOf(s))
				}
				if t.NumOut() == 2 {
					if t.Out(1) != typError {
						return errors.New("socket.io: \"" + k + "\": The second output of the callback function must be error interface")
					}
				} else if t.NumOut() != 1 {
					return errors.New("socket.io: \"" + k + "\": The callback function can only have 1 or 2 output values")
				}
				out := v.Call(in)
				if len(out) > 1 && !out[1].IsNil() {
					if err := out[1].Interface().(error); err != nil {
						return err
					}
				}
				data[k] = out[0].Interface()
				continue
			}
			data[k] = v
		}
	}
	if s.pid != "" {
		data["pid"] = s.pid
		data["offset"] = s.lastOffset
	}
	if len(data) > 0 {
		if err := pkt.SetData(data); err != nil {
			return err
		}
	}
	return s.send(pkt)
}

func (s *Socket) Connect(namespace string) (err error) {
	s.mux.Lock()
	defer s.mux.Unlock()

	if !s.status.CompareAndSwap(SocketClosed, SocketOpening) {
		panic("Socket.IO: socket is already connected to a namespce, multiple namespaces is TODO")
	}
	s.namespace = namespace
	if s.io.Connected() {
		if err = s.sendConnPkt(); err != nil {
			s.status.Store(SocketClosed)
			return
		}
	} else {
		s.status.Store(SocketClosed)
	}
	s.autoReconnect = true
	return
}

func (s *Socket) disconnected() {
	s.status.Store(SocketClosed)
}

func (s *Socket) OnConnect(cb func(s *Socket, namespace string)) {
	s.connectHandles.On(cb)
}

func (s *Socket) OnceConnect(cb func(s *Socket, namespace string)) {
	s.connectHandles.Once(cb)
}

func (s *Socket) OnDisconnect(cb func(s *Socket, namespace string)) {
	s.disconnectHandles.On(cb)
}

func (s *Socket) OnceDisconnect(cb func(s *Socket, namespace string)) {
	s.disconnectHandles.Once(cb)
}

func (s *Socket) OnBeforeConnect(cb func(s *Socket)) {
	s.beforeConnectHandles.On(func(s *Socket, _ struct{}) {
		cb(s)
	})
}

func (s *Socket) OnError(cb func(s *Socket, err error)) {
	s.errorHandles.On(cb)
}

func (s *Socket) OnceError(cb func(s *Socket, err error)) {
	s.errorHandles.Once(cb)
}

func (s *Socket) OnPacket(cb func(s *Socket, pkt *Packet)) {
	s.packetHandlers.On(cb)
}

func (s *Socket) OncePacket(cb func(s *Socket, pkt *Packet)) {
	s.packetHandlers.Once(cb)
}

func (s *Socket) OnMessage(cb func(event string, args []any)) {
	s.messageHandlers.On(cb)
}

func (s *Socket) OnceMessage(cb func(event string, args []any)) {
	s.messageHandlers.Once(cb)
}

func (s *Socket) Namespace() string {
	s.mux.RLock()
	defer s.mux.RUnlock()
	return s.namespace
}

func (s *Socket) Close() (err error) {
	if s.status.Load() == SocketClosed {
		return
	}

	s.mux.Lock()
	defer s.mux.Unlock()

	s.autoReconnect = false
	if s.status.Load() == SocketClosed {
		return
	}

	err = s.send(&Packet{
		typ:       DISCONNECT,
		namespace: s.namespace,
	})
	s.status.Store(SocketClosed)
	return
}

func (s *Socket) onError(err error) {
	s.errorHandles.Call(s, err)
}

func (s *Socket) onEvent(pkt *Packet) {
	if pkt.namespace != s.Namespace() {
		return
	}
	s.packetHandlers.Call(s, pkt)
	var arr []any
	if err := pkt.UnmarshalData(&arr); err == nil {
		if name, ok := arr[0].(string); ok {
			s.messageHandlers.Call(name, arr[1:])
		} else {
			s.onError(errNotString)
		}
	} else {
		s.onError(err)
	}
}

func (s *Socket) onAck(pkt *Packet) {
	s.ackMux.Lock()
	id := pkt.Id()
	ch, ok := s.ackChan[id]
	delete(s.ackChan, id)
	s.ackMux.Unlock()
	if ok {
		var arr [][]any
		if err := pkt.UnmarshalData(&arr); err != nil {
			s.onError(err)
			return
		}
		if len(arr) > 0 {
			ch <- arr[0]
		} else {
			ch <- nil
		}
	}
}

func (s *Socket) onBinary(_ *engine.Socket, data []byte) {
	pkt := &s.packet
	attachs := pkt.Attachments()

	if len(attachs) <= s.reconstructingAttach {
		s.onError(errRecvByte)
		return
	}

	attachs[s.reconstructingAttach] = append(make([]byte, 0, len(data)), data...)
	s.reconstructingAttach++
	if len(attachs) == s.reconstructingAttach {
		switch pkt.typ {
		case EVENT, BINARY_EVENT:
			s.onEvent(pkt)
		case ACK, BINARY_ACK:
			s.onAck(pkt)
		default:
			s.onError(&UnexpectedPacketTypeError{pkt.typ})
		}
	}
}

func (s *Socket) onMessage(_ *engine.Socket, data []byte) {
	pkt := &s.packet

	if len(pkt.Attachments()) > s.reconstructingAttach {
		s.onError(errRecvText)
		return
	}
	s.reconstructingAttach = 0

	if err := pkt.UnmarshalBinary(data); err != nil {
		s.onError(err)
		return
	}
	switch pkt.typ {
	case CONNECT:
		ok := s.Status() == SocketOpening && pkt.namespace == s.namespace
		if !ok {
			return
		}
		var obj struct {
			Sid string `json:"sid"`
			Pid string `json:"pid"`
		}
		if err := pkt.UnmarshalData(&obj); err != nil {
			return
		}
		s.mux.Lock()
		if true && len(s.msgbuf) == 0 { // TODO: socket.io retrive
			s.ackId = 0
		}
		s.sid = obj.Sid
		s.pid = obj.Pid
		for _, bts := range s.msgbuf {
			s.io.Emit(bts)
		}
		s.msgbuf = s.msgbuf[:0]
		s.status.Store(SocketConnected)
		s.mux.Unlock()
		s.connectHandles.Call(s, pkt.namespace)
	case DISCONNECT:
		s.disconnected()
		s.disconnectHandles.Call(s, pkt.namespace)
	case EVENT, BINARY_EVENT:
		if len(pkt.Attachments()) == 0 {
			s.onEvent(pkt)
		}
	case ACK, BINARY_ACK:
		if len(pkt.Attachments()) == 0 {
			s.onAck(pkt)
		}
	case CONNECT_ERROR:
		var reason string
		if err := pkt.UnmarshalData(&reason); err != nil {
			s.onError(err)
			return
		}
		s.onError(&ConnectError{reason})
	default:
		s.onError(&UnexpectedPacketTypeError{pkt.typ})
	}
}

func (s *Socket) send(pkt *Packet) (err error) {
	var buf bytes.Buffer
	if _, err = pkt.WriteTo(&buf); err != nil {
		return
	}
	bts := buf.Bytes()
	if s.Status() == SocketConnected {
		s.io.Emit(bts)
	} else {
		switch pkt.typ {
		case EVENT, BINARY_EVENT, ACK, BINARY_ACK:
			s.mux.Lock()
			s.msgbuf = append(s.msgbuf, bts)
			s.mux.Unlock()
		default:
			if s.io.Connected() {
				s.io.Emit(bts)
			}
		}
	}
	return
}

func (s *Socket) Emit(event string, args ...any) (err error) {
	pkt := Packet{
		typ:       EVENT,
		namespace: s.namespace,
	}
	argsAll := make([]any, 1+len(args))
	argsAll[0] = event
	copy(argsAll[1:], args)
	if err = pkt.SetData(argsAll...); err != nil {
		return
	}
	return s.send(&pkt)
}

func (s *Socket) assignAckId() (id int, res <-chan []any) {
	s.ackMux.Lock()
	defer s.ackMux.Unlock()
	for {
		id = s.ackId
		s.ackId = (s.ackId + 1) & 0x3fffffff
		if _, ok := s.ackChan[id]; !ok {
			break
		}
	}
	ch := make(chan []any, 1)
	s.ackChan[id] = ch
	res = ch
	return
}

func (s *Socket) EmitWithAck(event string, args ...any) (<-chan []any, error) {
	pkt := &Packet{
		typ:       EVENT,
		namespace: s.namespace,
	}
	argsAll := make([]any, 1+len(args))
	argsAll[0] = event
	copy(argsAll[1:], args)
	if err := pkt.SetData(argsAll...); err != nil {
		return nil, err
	}
	id, res := s.assignAckId()
	pkt.SetId(id)
	if err := s.send(pkt); err != nil {
		s.ackMux.Lock()
		delete(s.ackChan, id)
		s.ackMux.Unlock()
		return nil, err
	}
	return res, nil
}
