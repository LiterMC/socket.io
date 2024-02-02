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
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LiterMC/socket.io/internal/utils"
	"github.com/gorilla/websocket"
)

const Protocol = 4

var (
	errMultipleOpen = errors.New("Engine.IO: socket was already opened")

	ErrSocketConnected = errors.New("Engine.IO: socket was already connected")
	ErrPingTimeout     = errors.New("Engine.IO: did not receive PING packet for a long time")
)

type SocketStatus = int32

const (
	SocketClosed SocketStatus = iota
	SocketOpening
	SocketConnected
)

var WebsocketDialer *websocket.Dialer = &websocket.Dialer{
	Proxy:            http.ProxyFromEnvironment,
	HandshakeTimeout: 30 * time.Second,
}

type Socket struct {
	Dialer *websocket.Dialer
	opts   Options
	url    url.URL

	mux     sync.RWMutex
	dialCtx context.Context
	ctx     context.Context
	cancel  context.CancelCauseFunc

	connectHandles    utils.HandlerList[*Socket, struct{}]
	disconnectHandles utils.HandlerList[*Socket, error]
	pongHandles       utils.HandlerList[*Socket, []byte]
	binaryHandlers    utils.HandlerList[*Socket, []byte]
	messageHandles    utils.HandlerList[*Socket, []byte]

	wsconn        *websocket.Conn
	status        atomic.Int32
	sid           string
	pingInterval  time.Duration
	pingTimeout   time.Duration
	maxPayload    int
	reDialTimeout time.Duration

	notifyCh chan struct{}
	msgCh    chan *Packet
	msgbuf   []*Packet
}

type Options struct {
	Host         string // <host>:<port>
	Path         string
	Secure       bool
	ExtraQuery   url.Values
	ExtraHeaders http.Header
	DialTimeout  time.Duration
}

var DefaultOption = Options{
	Path:   "/engine.io",
	Secure: true,
}

func NewSocket(opts Options) (s *Socket, err error) {
	dialURL := url.URL{
		Host: opts.Host,
		Path: opts.Path,
	}
	if opts.Secure {
		dialURL.Scheme = "wss"
	} else {
		dialURL.Scheme = "ws"
	}
	query := make(url.Values, 2+len(opts.ExtraQuery))
	for k, v := range opts.ExtraQuery {
		query[k] = v
	}
	query.Set("EIO", strconv.Itoa(Protocol))
	query.Set("transport", "websocket")
	dialURL.RawQuery = query.Encode()

	s = &Socket{
		Dialer:   WebsocketDialer,
		opts:     opts,
		url:      dialURL,
		notifyCh: make(chan struct{}, 1),
		msgCh:    make(chan *Packet, 0),
	}
	return
}

func (s *Socket) Status() SocketStatus {
	return s.status.Load()
}

func (s *Socket) Connected() bool {
	return s.Status() == SocketConnected
}

func (s *Socket) ID() string {
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

func (s *Socket) dial(ctx context.Context) (err error) {
	var wsconn *websocket.Conn
	if s.opts.DialTimeout > 0 {
		tctx, cancel := context.WithTimeout(ctx, s.opts.DialTimeout)
		wsconn, _, err = s.Dialer.DialContext(tctx, s.url.String(), s.opts.ExtraHeaders)
		cancel()
	} else {
		wsconn, _, err = s.Dialer.DialContext(ctx, s.url.String(), s.opts.ExtraHeaders)
	}
	if err != nil {
		return
	}
	s.ctx, s.cancel = context.WithCancelCause(s.dialCtx)
	s.wsconn = wsconn
	s.msgbuf = s.msgbuf[:0]
	s.reDialTimeout = time.Second

	wsconn.SetCloseHandler(s.wsCloseHandler)

	return
}

func (s *Socket) Dial(ctx context.Context) (err error) {
	if s.status.Load() != SocketClosed {
		return ErrSocketConnected
	}

	s.mux.Lock()
	defer s.mux.Unlock()

	if s.status.CompareAndSwap(SocketClosed, SocketOpening) || s.wsconn != nil {
		return ErrSocketConnected
	}

	s.dialCtx = ctx
	if err = s.dial(ctx); err != nil {
		s.status.Store(SocketClosed)
		return
	}

	go s._reader(s.ctx, s.wsconn)

	return
}

func (s *Socket) reDial() (err error) {
	if s.status.Load() != SocketClosed {
		return ErrSocketConnected
	}

	s.mux.Lock()
	defer s.mux.Unlock()

	if s.status.CompareAndSwap(SocketClosed, SocketOpening) {
		return ErrSocketConnected
	}

	if err = s.dial(s.dialCtx); err != nil {
		s.status.Store(SocketClosed)
		return
	}

	go s._reader(s.ctx, s.wsconn)

	return
}

func (s *Socket) onMessage(data []byte) {
	s.messageHandles.Call(s, data)
}

func (s *Socket) onClose(err error) {
	s.mux.RLock()
	if wsconn := s.wsconn; wsconn != nil {
		wsconn.SetCloseHandler(nil)
		wsconn.Close()
	}
	s.cancel(err)
	dialCtx := s.dialCtx
	s.mux.RUnlock()

	if s.status.Swap(SocketClosed) != SocketClosed && err != nil {
		if s.reDialTimeout < time.Minute*5 {
			s.reDialTimeout = s.reDialTimeout * 2
		}
		go func(ctx context.Context, timeout time.Duration) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(timeout):
				s.reDial()
			}
		}(dialCtx, s.reDialTimeout)
	}

	s.disconnectHandles.Call(s, err)
}

func (s *Socket) OnConnect(cb func(s *Socket)) {
	s.connectHandles.On(func(s *Socket, _ struct{}) {
		cb(s)
	})
	// TODO: Or maybe?
	// ptr := new(func(s *Socket))
	// *ptr = cb
	// s.connectHandles.On(*(func(*Socket, struct{}))((unsafe.Pointer)(ptr)))
}

func (s *Socket) OnceConnect(cb func(s *Socket)) {
	if s.Connected() {
		cb(s)
	} else {
		s.connectHandles.Once(func(s *Socket, _ struct{}) {
			cb(s)
		})
	}
}

func (s *Socket) OnDisconnect(cb func(s *Socket, err error)) {
	s.disconnectHandles.On(cb)
}

func (s *Socket) OnceDisconnect(cb func(s *Socket, err error)) {
	s.disconnectHandles.Once(cb)
}

func (s *Socket) OnPong(cb func(s *Socket, data []byte)) {
	s.pongHandles.On(cb)
}

func (s *Socket) OncePong(cb func(s *Socket, data []byte)) {
	s.pongHandles.Once(cb)
}

func (s *Socket) OnBinary(cb func(s *Socket, data []byte)) {
	s.binaryHandlers.On(cb)
}

func (s *Socket) OnceBinary(cb func(s *Socket, data []byte)) {
	s.binaryHandlers.Once(cb)
}

func (s *Socket) OnMessage(cb func(s *Socket, data []byte)) {
	s.messageHandles.On(cb)
}

func (s *Socket) OnceMessage(cb func(s *Socket, data []byte)) {
	s.messageHandles.Once(cb)
}

func (s *Socket) wsCloseHandler(code int, text string) error {
	s.status.Store(SocketClosed)

	wer := &websocket.CloseError{Code: code, Text: text}
	if code == websocket.CloseNormalClosure {
		s.onClose(nil)
	} else {
		s.onClose(wer)
	}
	return nil
}

func (s *Socket) _reader(ctx context.Context, wsconn *websocket.Conn) {
	defer wsconn.Close()

	openCh := make(chan struct{}, 0)
	pingCh := make(chan struct{}, 1)

	go func() {
		defer wsconn.Close()
		select {
		case <-ctx.Done():
			return
		case <-openCh:
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.pingInterval + s.pingTimeout):
				s.onClose(ErrPingTimeout)
				return
			case <-pingCh:
			}
		}
	}()

	pkt := new(Packet)
	var buf []byte
	for {
		code, r, err := wsconn.NextReader()
		if err != nil {
			s.mux.RLock()
			ok := wsconn == s.wsconn
			s.mux.RUnlock()
			if ok {
				s.onClose(err)
			}
			return
		}

		// reset ping timer
		select {
		case pingCh <- struct{}{}:
		default:
		}

		switch code {
		case websocket.BinaryMessage:
			if buf, err = utils.ReadAllTo(r, buf[:0]); err != nil {
				s.onClose(err)
				return
			}
			s.binaryHandlers.Call(s, buf)
			continue
		case websocket.TextMessage:
			if buf, err = utils.ReadAllTo(r, buf[:0]); err != nil {
				s.onClose(err)
				return
			}
			if len(buf) > 0 && buf[0] == 'b' {
				n, err := base64.StdEncoding.Decode(buf, buf[1:])
				if err != nil {
					s.onClose(err)
					return
				}
				s.binaryHandlers.Call(s, buf[:n])
				continue
			}
		default:
			continue
		}

		if err = pkt.UnmarshalBinary(buf); err != nil {
			s.onClose(err)
			return
		}

		switch pkt.typ {
		case OPEN:
			if s.Status() != SocketOpening {
				s.onClose(errMultipleOpen)
				return
			}
			var obj struct {
				Sid          string   `json:"sid"`
				Upgrades     []string `json:"upgrades"`
				PingInterval int      `json:"pingInterval"`
				PingTimeout  int      `json:"pingTimeout"`
				MaxPayload   int      `json:"maxPayload"`
			}
			if err := pkt.UnmarshalBody(&obj); err != nil {
				s.onClose(err)
				break
			}

			s.mux.Lock()
			s.sid = obj.Sid
			s.pingInterval = (time.Duration)(obj.PingInterval) * time.Millisecond
			s.pingTimeout = (time.Duration)(obj.PingTimeout) * time.Millisecond
			s.maxPayload = obj.MaxPayload
			for _, pkt := range s.msgbuf {
				sendPkt(wsconn, pkt)
			}
			s.msgbuf = s.msgbuf[:0]
			s.status.Store(SocketConnected)
			s.mux.Unlock()

			close(openCh)

			s.connectHandles.Call(s, struct{}{})
		case CLOSE:
			s.onClose(nil)
			return
		case PING:
			pkt.typ = PONG
			s.send(pkt)
		case PONG:
			s.pongHandles.Call(s, pkt.body)
		case MESSAGE:
			s.onMessage(pkt.body)
		default:
			s.onClose(fmt.Errorf("Engine.IO: unsupported packet type %s", pkt.typ))
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

func (s *Socket) Close() error {
	s.send(&Packet{
		typ: CLOSE,
	})
	return nil
}

func (s *Socket) send(pkt *Packet) {
	s.mux.Lock()
	if s.Status() != SocketConnected {
		s.msgbuf = append(s.msgbuf, pkt)
		s.mux.Unlock()
		return
	}
	wsconn := s.wsconn
	s.mux.Unlock()

	if err := sendPkt(wsconn, pkt); err != nil {
		s.onClose(err)
	}
	return
}

func (s *Socket) Emit(body []byte) {
	s.send(&Packet{
		typ:  MESSAGE,
		body: body,
	})
}
