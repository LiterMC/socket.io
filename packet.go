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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/LiterMC/socket.io/internal/utils"
)

var errTooMuchArgs = errors.New("Too much arguments were given")

type PacketType int8

const (
	unknownType PacketType = -1
	CONNECT     PacketType = iota
	DISCONNECT
	EVENT
	ACK
	CONNECT_ERROR
	BINARY_EVENT
	BINARY_ACK
)

func (t PacketType) String() string {
	switch t {
	case CONNECT:
		return "CONNECT"
	case DISCONNECT:
		return "DISCONNECT"
	case EVENT:
		return "EVENT"
	case ACK:
		return "ACK"
	case CONNECT_ERROR:
		return "CONNECT_ERROR"
	case BINARY_EVENT:
		return "BINARY_EVENT"
	case BINARY_ACK:
		return "BINARY_ACK"
	}
	return fmt.Sprintf("PacketType(%d)", (int8)(t))
}

func (t PacketType) ID() byte {
	switch t {
	case CONNECT:
		return '0'
	case DISCONNECT:
		return '1'
	case EVENT:
		return '2'
	case ACK:
		return '3'
	case CONNECT_ERROR:
		return '4'
	case BINARY_EVENT:
		return '5'
	case BINARY_ACK:
		return '6'
	}
	panic(fmt.Errorf("Unknown socket.io packet type %d", t))
}

func pktTypeFromByte(id byte) PacketType {
	switch id {
	case '0':
		return CONNECT
	case '1':
		return DISCONNECT
	case '2':
		return EVENT
	case '3':
		return ACK
	case '4':
		return CONNECT_ERROR
	case '5':
		return BINARY_EVENT
	case '6':
		return BINARY_ACK
	}
	return unknownType
}

type Packet struct {
	typ       PacketType
	namespace string
	id        int64
	data      []any
	attachs []io.Reader
}

func (p *Packet) Type() PacketType {
	return p.typ
}

func (p *Packet) String() string {
	return fmt.Sprintf("Packet(%s, %q, %d, <%d bytes>)", p.typ.String(), p.namespace, p.id, len(p.data))
}

func (p *Packet) SetData(args ...any) {
	switch p.typ {
	case BINARY_EVENT, BINARY_ACK:
		// TODO: replace placeholder for BINARY_EVENT and BINARY_ACK
		fallthrough
	case EVENT, ACK:
	default:
		if len(args) > 1 {
			panic(errTooMuchArgs)
		}
	}
	p.data = args
	return
}

func (p *Packet) WriteTo(w io.Writer) (n int64, err error) {
	var n0 int
	if err = utils.WriteByte(w, p.typ.ID()); err != nil {
		return
	}
	n++
	if p.namespace != "" && p.namespace != "/" {
		if n0, err = io.WriteString(w, p.namespace); err != nil {
			return
		}
		n += (int64)(n0)
		if err = utils.WriteByte(w, ','); err != nil {
			return
		}
		n++
	}
	if p.id > 0 {
		var buf [10]byte
		n0, err = w.Write(strconv.AppendInt(buf[:0], p.id, 10))
		n += (int64)(n0)
		if err != nil {
			return
		}
	}
	if len(p.data) > 0 {
		json.NewEncoder(w).Encode(p.data)
	}
	return
}

func (p *Packet) Reset(r io.Reader)(err error){
	return
}
