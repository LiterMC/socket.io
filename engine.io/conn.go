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

package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const Protocol = 4

var (
	ErrSocketConnected = errors.New("Engine.IO: socket was already connected")
	ErrPingTimeout = errors.New("Engine.IO: did not receive PING packet for a long time")
)

const (
	SocketIdle int32 = iota
	SocketUpgrading
	SocketConnected
)

var WebsocketDialer *websocket.Dialer = &websocket.Dialer{
	Proxy:            http.ProxyFromEnvironment,
	HandshakeTimeout: 30 * time.Second,
}

type Socket struct {
	Dialer      *websocket.Dialer
	url         *url.URL
	query       url.Values
	header      http.Header
	dialTimeout time.Duration

	mux    sync.RWMutex
	ctx    context.Context
	cancel context.CancelCauseFunc

	ConnectHandle    func(s *Socket)
	DisconnectHandle func(s *Socket)
	ErrorHandle      func(s *Socket, err error)
	PongHandle       func(s *Socket, pkt *Packet)
	MessageHandle    func(s *Socket, pkt *Packet)

	wsconn       *websocket.Conn
	status       int32
	sid          string
	pingInterval time.Duration
	pingTimeout  time.Duration
	maxPayload   int

	notifyCh chan struct{}
	msgCh    chan *Packet
	msgbuf   []*Packet
}

type SocketOptions func(s *Socket)

func WithQuery(query url.Values) SocketOptions {
	return func(s *Socket) {
		s.query = query
	}
}

func WithHeader(header http.Header) SocketOptions {
	return func(s *Socket) {
		s.header = header
	}
}

func WithDialTimeout(timeout time.Duration) SocketOptions {
	return func(s *Socket) {
		s.dialTimeout = timeout
	}
}

func NewSocket(path string, opts ...SocketOptions) (s *Socket, err error) {
	s = &Socket{
		Dialer:   WebsocketDialer,
		notifyCh: make(chan struct{}, 1),
		msgCh:    make(chan *Packet, 0),
	}

	if s.url, err = url.Parse(path); err != nil {
		return
	}

	for _, opt := range opts {
		opt(s)
	}

	if s.query == nil {
		s.query = make(url.Values, 2)
	}
	s.query.Set("EIO", strconv.Itoa(Protocol))
	s.query.Set("transport", "websocket")
	s.url.RawQuery = s.query.Encode()
	return
}

func (s *Socket) Status() int32 {
	s.mux.RLock()
	defer s.mux.RUnlock()
	return s.status
}

func (s *Socket) Connected() bool {
	return s.Status() == SocketConnected
}

func (s *Socket) Id() string {
	s.mux.RLock()
	defer s.mux.RUnlock()
	return s.sid
}

func (s *Socket) Context() context.Context {
	s.mux.RLock()
	defer s.mux.RUnlock()
	return s.ctx
}

func (s *Socket) Conn() *websocket.Conn {
	s.mux.RLock()
	defer s.mux.RUnlock()
	return s.wsconn
}

func (s *Socket) Dial(ctx context.Context) (err error) {
	s.mux.Lock()
	defer s.mux.Unlock()

	if s.status != SocketIdle {
		return ErrSocketConnected
	}

	hasSid := s.sid != ""
	if hasSid {
		s.query.Set("sid", s.sid)
	}
	s.url.RawQuery = s.query.Encode()
	if hasSid {
		s.query.Del("sid")
	}

	var wsconn *websocket.Conn
	if s.dialTimeout > 0 {
		tctx, cancel := context.WithTimeout(ctx, s.dialTimeout)
		wsconn, _, err = s.Dialer.DialContext(tctx, s.url.String(), s.header)
		cancel()
	} else {
		wsconn, _, err = s.Dialer.DialContext(ctx, s.url.String(), s.header)
	}
	if err != nil {
		s.status = SocketIdle
		return
	}
	s.ctx, s.cancel = context.WithCancelCause(ctx)
	s.wsconn = wsconn
	s.status = SocketUpgrading

	wsconn.SetCloseHandler(s.wsCloseHandler)

	go s._reader(s.ctx, wsconn)
	go s._writer(s.ctx, wsconn)

	return
}

func (s *Socket) onError(err error) {
	if s.ErrorHandle != nil {
		s.ErrorHandle(s, err)
	}
}

func (s *Socket) onMessage(pkt *Packet) {
	if s.MessageHandle != nil {
		s.MessageHandle(s, pkt)
	}
}

func (s *Socket) wsCloseHandler(code int, text string) error {
	s.mux.Lock()
	s.status = SocketIdle
	s.mux.Unlock()

	wer := &websocket.CloseError{Code: code, Text: text}
	if s.DisconnectHandle != nil {
		s.DisconnectHandle(s)
	}
	if code != websocket.CloseNormalClosure {
		s.onError(wer)
		w := time.Second
		for i := 0; i < 10; i++ {
			err := s.Dial(s.ctx)
			if err == nil || err == ErrSocketConnected {
				return nil
			}
			s.onError(err)
			time.Sleep(w)
			if w < 10*time.Minute {
				w *= 2
			}
		}
	}
	return nil
}

func (s *Socket) _reader(ctx context.Context, wsconn *websocket.Conn) {
	defer wsconn.Close()

	pingCh := make(chan struct{}, 1)

	go func(){
		defer wsconn.Close()
		select {
		case <-ctx.Done():
			return
		case <-pingCh:
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.pingInterval + s.pingTimeout):
				s.onError(ErrPingTimeout)
				return
			case <-pingCh:
			}
		}
	}()

	pkt := new(Packet)
	for {
		code, r, err := wsconn.NextReader()
		if err != nil {
			s.onError(err)
			return
		}
		if code != websocket.TextMessage {
			continue
		}
		_, err = pkt.ReadFrom(r)
		if err != nil {
			s.onError(err)
			continue
		}
		switch pkt.typ {
		case OPEN:
			var obj struct {
				Sid          string   `json:"sid"`
				Upgrades     []string `json:"upgrades"`
				PingInterval int      `json:"pingInterval"`
				PingTimeout  int      `json:"pingTimeout"`
				MaxPayload   int      `json:"maxPayload"`
			}
			if err = pkt.UnmarshalBody(&obj); err != nil {
				s.onError(err)
				break
			}
			s.sid = obj.Sid
			s.pingInterval = (time.Duration)(obj.PingInterval) * time.Millisecond
			s.pingTimeout = (time.Duration)(obj.PingTimeout) * time.Millisecond
			s.maxPayload = obj.MaxPayload
			if s.ConnectHandle != nil {
				s.ConnectHandle(s)
			}
			select {
			case pingCh <- struct{}{}:
			default:
			}
		case CLOSE:
			if s.DisconnectHandle != nil {
				s.DisconnectHandle(s)
			}
			s.Close()
			return
		case PING:
			select {
			case pingCh <- struct{}{}:
			default:
			}
			pkt.typ = PONG
			s.emit(pkt)
		case PONG:
			if s.PongHandle != nil {
				s.PongHandle(s, pkt)
			}
		case MESSAGE:
			s.onMessage(pkt)
		default:
			s.onError(fmt.Errorf("Engine.IO: unsupported packet type %s", pkt.typ))
		}
		if err != nil {
			s.onError(err)
		}
	}
}

func sendPkt(wsconn *websocket.Conn, pkt *Packet) (err error) {
	var w io.WriteCloser
	if w, err = wsconn.NextWriter(websocket.TextMessage); err != nil {
		return
	}
	defer w.Close()
	_, err = pkt.WriteTo(w)
	if err != nil {
		return
	}
	return
}

func (s *Socket) _writer(ctx context.Context, wsconn *websocket.Conn) {
	defer wsconn.Close()

	var msgbuf []*Packet
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.notifyCh:
			s.mux.Lock()
			msgbuf, s.msgbuf = s.msgbuf, msgbuf[:0]
			s.mux.Unlock()
			for i, pkt := range msgbuf {
				if err := sendPkt(wsconn, pkt); err != nil {
					s.onError(err)
					msgbuf = msgbuf[:copy(msgbuf, msgbuf[i:])]
					s.mux.Lock()
					msgbuf, s.msgbuf = s.msgbuf, append(msgbuf, s.msgbuf...)
					s.mux.Unlock()
					break
				}
			}
		case pkt := <-s.msgCh:
			s.mux.Lock()
			msgbuf, s.msgbuf = s.msgbuf, msgbuf[:0]
			s.mux.Unlock()
			ok := pkt != nil
			for i, pkt := range msgbuf {
				if err := sendPkt(wsconn, pkt); err != nil {
					s.onError(err)
					msgbuf = msgbuf[:copy(msgbuf, msgbuf[i:])]
					s.mux.Lock()
					msgbuf, s.msgbuf = s.msgbuf, append(msgbuf, s.msgbuf...)
					if ok {
						ok = false
						s.msgbuf = append(s.msgbuf, pkt)
					}
					s.mux.Unlock()
					break
				}
			}
			if ok {
				if err := sendPkt(wsconn, pkt); err != nil {
					s.onError(err)
				}
			}
		}
	}
}

func (s *Socket) Close() error {
	s.emit(&Packet{
		typ: CLOSE,
	})
	return nil
}

func (s *Socket) refreshPackets() {
	select {
	case s.notifyCh <- struct{}{}:
	default:
	}
}

func (s *Socket) emit(pkt *Packet) {
	select {
	case s.msgCh <- pkt:
	default:
		s.mux.Lock()
		s.msgbuf = append(s.msgbuf, pkt)
		s.mux.Unlock()
		s.refreshPackets()
	}
}

func (s *Socket) Emit(body []byte) {
	s.emit(&Packet{
		typ: MESSAGE,
		body: body,
	})
}
