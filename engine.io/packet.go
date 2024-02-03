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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
)

type UnexpectedPacketTypeError struct {
	Type PacketType
}

var _ error = (*UnexpectedPacketTypeError)(nil)

func (e *UnexpectedPacketTypeError) Error() string {
	return fmt.Sprintf("Unexpected packet type %d", e.Type)
}

type PacketType int8

const (
	unknownType PacketType = -1

	OPEN PacketType = iota
	CLOSE
	PING
	PONG
	MESSAGE
	UPGRADE
	NOOP

	BINARY PacketType = 'b'
)

func (t PacketType) String() string {
	switch t {
	case OPEN:
		return "OPEN"
	case CLOSE:
		return "CLOSE"
	case PING:
		return "PING"
	case PONG:
		return "PONG"
	case MESSAGE:
		return "MESSAGE"
	case UPGRADE:
		return "UPGRADE"
	case NOOP:
		return "NOOP"
	case BINARY:
		return "BINARY"
	}
	return fmt.Sprintf("PacketType(%d)", (int8)(t))
}

func (t PacketType) ID() byte {
	switch t {
	case OPEN:
		return '0'
	case CLOSE:
		return '1'
	case PING:
		return '2'
	case PONG:
		return '3'
	case MESSAGE:
		return '4'
	case UPGRADE:
		return '5'
	case NOOP:
		return '6'
	case BINARY:
		return 'b'
	}
	panic(&UnexpectedPacketTypeError{t})
}

func pktTypeFromByte(id byte) PacketType {
	switch id {
	case '0':
		return OPEN
	case '1':
		return CLOSE
	case '2':
		return PING
	case '3':
		return PONG
	case '4':
		return MESSAGE
	case '5':
		return UPGRADE
	case '6':
		return NOOP
	case 'b':
		return BINARY
	}
	return unknownType
}

type Packet struct {
	typ  PacketType
	body []byte
}

func (p *Packet) Type() PacketType {
	return p.typ
}

func (p *Packet) String() string {
	return fmt.Sprintf("Packet(%s, <%d bytes>)", p.typ.String(), len(p.body))
}

func (p *Packet) Body() []byte {
	return p.body
}

func (p *Packet) SetBody(body []byte) {
	p.body = body
}

func (p *Packet) UnmarshalBody(ptr any) error {
	return json.Unmarshal(p.body, &ptr)
}

func (p *Packet) MarshalBinary() (data []byte, err error) {
	data = make([]byte, 1+len(p.body))
	data[0] = p.typ.ID()
	copy(data[1:], p.body)
	return
}

func (p *Packet) UnmarshalBinary(data []byte) error {
	if len(data) == 0 {
		return io.EOF
	}

	typ := pktTypeFromByte(data[0])
	if typ == unknownType {
		return &UnexpectedPacketTypeError{typ}
	}
	p.typ = typ
	if typ == BINARY {
		n, err := base64.StdEncoding.Decode(data, data[1:])
		if err != nil {
			return err
		}
		data = data[:n]
	} else {
		data = data[1:]
	}
	// reuse buffer if possible
	p.body = append(p.body[:0], data...)
	return nil
}
