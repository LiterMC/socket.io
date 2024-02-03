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
	"errors"
	"sync"

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

type SocketStatus int32

const (
	SocketClosed SocketStatus = iota
	SocketOpening
	SocketConnected
)

type Socket struct {
	io *engine.Socket

	mux           sync.RWMutex
	status        SocketStatus
	sid, pid      string
	namespace     string
	autoReconnect bool

	packet               Packet
	reconstructingAttach int

	ackMux  sync.Mutex
	ackId   int
	ackChan map[int]chan []any

	connectHandles    utils.HandlerList[*Socket, string]
	disconnectHandles utils.HandlerList[*Socket, string]
	errorHandles      utils.HandlerList[*Socket, error]
	packetHandlers    utils.HandlerList[*Socket, *Packet]
	messageHandlers   utils.HandlerList[string, []any]

	msgbuf [][]byte
}

func NewSocket(io *engine.Socket) (s *Socket) {
	s = &Socket{
		io: io,

		ackChan: make(map[int]chan []any),
	}

	shouldReconnect := false
	io.OnConnect(func(*engine.Socket) {
		s.mux.RLock()
		reconnect := s.autoReconnect
		s.mux.RUnlock()
		if shouldReconnect && reconnect {
			if err := s.send(&Packet{
				typ:       CONNECT,
				namespace: s.namespace,
			}); err != nil {
				s.onError(err)
			}
			shouldReconnect = false
		}
	})
	io.OnDisconnect(func(_ *engine.Socket, err error) {
		s.disconnected()
		if err != nil {
			shouldReconnect = true
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
	s.mux.RLock()
	defer s.mux.RUnlock()
	return s.status
}

func (s *Socket) Connect(namespace string) (err error) {
	s.mux.Lock()
	defer s.mux.Unlock()
	if s.status != SocketClosed {
		panic("Socket.IO: socket is already connected to a namespce, multiple namespaces is TODO")
	}
	s.namespace = namespace
	if err = s.send(&Packet{
		typ:       CONNECT,
		namespace: namespace,
	}); err != nil {
		return
	}
	s.status = SocketOpening
	s.autoReconnect = true
	return
}

func (s *Socket) disconnected() {
	s.mux.Lock()
	defer s.mux.Unlock()
	s.status = SocketClosed
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
	s.disconnectHandles.On(cb)
}

func (s *Socket) OnError(cb func(s *Socket, err error)) {
	s.errorHandles.On(cb)
}

func (s *Socket) OnceError(cb func(s *Socket, err error)) {
	s.errorHandles.On(cb)
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
	s.mux.Lock()
	defer s.mux.Unlock()

	s.autoReconnect = false
	if s.status == SocketClosed {
		return
	}

	err = s.send(&Packet{
		typ:       DISCONNECT,
		namespace: s.namespace,
	})
	s.status = SocketClosed
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
			s.messageHandlers.Call(name, arr)
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
		s.mux.RLock()
		ok := s.status == SocketOpening && pkt.namespace == s.namespace
		s.mux.RUnlock()
		if !ok {
			return
		}
		var obj struct {
			Sid string `json:"sid"`
			Pid string `json:"pid"`
		}
		if err := pkt.UnmarshalData(&obj); err == nil {
			s.mux.Lock()
			s.status = SocketConnected
			s.sid = obj.Sid
			s.pid = obj.Pid
			for _, bts := range s.msgbuf {
				s.io.Emit(bts)
			}
			s.msgbuf = s.msgbuf[:0]
			s.mux.Unlock()
			s.connectHandles.Call(s, pkt.namespace)
		}
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
	if s.io.Status() == engine.SocketConnected {
		switch pkt.typ {
		case EVENT, BINARY_EVENT, ACK, BINARY_ACK:
			s.mux.Lock()
			s.msgbuf = append(s.msgbuf, bts)
			s.mux.Unlock()
		}
	} else {
		s.io.Emit(bts)
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
	pkt := Packet{
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
	if err := s.send(&pkt); err != nil {
		s.ackMux.Lock()
		delete(s.ackChan, id)
		s.ackMux.Unlock()
		return nil, err
	}
	return res, nil
}
