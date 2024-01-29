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
	"fmt"
	"io"
	"strconv"

	"github.com/LiterMC/socket.io/internal/utils"
)

var (
	errTooMuchArgs      = errors.New("Too much arguments were given")
	errIncompletePacket = errors.New("Incomplete packet")
)

type UnexpectedPacketTypeError struct {
	Type PacketType
}

var _ error = (*UnexpectedPacketTypeError)(nil)

func (e *UnexpectedPacketTypeError) Error() string {
	return fmt.Sprintf("Unexpected packet type %d", e.Type)
}

type UnexpectedTokenError struct {
	Token byte
}

var _ error = (*UnexpectedTokenError)(nil)

func (e *UnexpectedTokenError) Error() string {
	return fmt.Sprintf("Unexpected token %q", e.Token)
}

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
	id        int
	data      []byte
	attachs   [][]byte
}

func (p *Packet) Type() PacketType {
	return p.typ
}

func (p *Packet) String() string {
	return fmt.Sprintf("Packet(%s, %q, %d, <%d bytes>)", p.typ.String(), p.namespace, p.id, len(p.data))
}

func (p *Packet) SetData(args ...any) (err error) {
	p.data = nil
	p.attachs = p.attachs[:0]
	if len(args) == 0 {
		return
	}
	switch p.typ {
	case EVENT, ACK, BINARY_EVENT, BINARY_ACK:
		p.encodeAttachs(args)
		if len(p.attachs) > 0 {
			switch p.typ {
			case EVENT:
				p.typ = BINARY_EVENT
			case ACK:
				p.typ = BINARY_ACK
			}
		}
		p.data, err = json.Marshal(args)
	default:
		if len(args) > 1 {
			panic(errTooMuchArgs)
		}
		p.data, err = json.Marshal(args[0])
	}
	return
}

func (p *Packet) UnmarshalData(ptr any) (err error) {
	if err = json.Unmarshal(p.data, ptr); err != nil {
		return
	}
	p.decodeAttachs(ptr)
	return
}

func (p *Packet) Attachments() [][]byte {
	return p.attachs
}

func (p *Packet) WriteTo(w io.Writer) (n int64, err error) {
	var n0 int
	if err = utils.WriteByte(w, p.typ.ID()); err != nil {
		return
	}
	n++
	var buf [10]byte
	if attLeng := len(p.attachs); attLeng > 0 {
		n0, err = w.Write(append(strconv.AppendInt(buf[:0], (int64)(attLeng), 10), '-'))
		n += (int64)(n0)
		if err != nil {
			return
		}
	}
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
		n0, err = w.Write(strconv.AppendInt(buf[:0], (int64)(p.id), 10))
		n += (int64)(n0)
		if err != nil {
			return
		}
	}
	if len(p.data) > 0 {
		n0, err = w.Write(p.data)
		n += (int64)(n0)
		if err != nil {
			return
		}
	}
	return
}

func (p *Packet) UnmarshalBinary(data []byte) (err error) {
	if len(data) == 0 {
		return io.EOF
	}
	typ := pktTypeFromByte(data[0])
	if typ == unknownType {
		err = &UnexpectedPacketTypeError{typ}
		return
	}
	p.typ = typ
	p.namespace = ""
	p.id = 0
	p.data = p.data[:0]
	p.attachs = p.attachs[:0]

	if data = data[1:]; len(data) == 0 {
		return
	}

	var (
		num int

		attachLen int
	)
	num, data = readNumber(data)
	if num >= 0 { // if read a number
		if ok := len(data) > 0; ok && data[0] == '-' { // is <# of binary attachments>-
			attachLen = num
		} else { // assume it is <acknowledgment id>
			p.id = num
			if ok {
				goto READ_DATA
			}
			return
		}
	}
	if data[0] == '/' { // is <namespace>,
		i := bytes.IndexByte(data, ',')
		if i < 0 {
			return io.EOF
		}
		p.namespace, data = (string)(data[:i]), data[i+1:]
	}
READ_DATA:
	p.data = append(p.data, data...)
	if cap(p.attachs) >= attachLen {
		p.attachs = p.attachs[:attachLen]
	} else {
		p.attachs = make([][]byte, attachLen)
	}
	return
}

func readNumber(data []byte) (num int, rest []byte) {
	if len(data) == 0 || data[0] < '0' || '9' < data[0] {
		return -1, data
	}
	for {
		if len(data) == 0 {
			return
		}
		b := data[0]
		if b < '0' || '9' < b {
			rest = data
			return
		}
		data = data[1:]
		num = num*10 + (int)(b-'0')
	}
}
