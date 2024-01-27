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
	"fmt"
)

type PacketType int8

const (
	unknownType PacketType = -1
	OPEN        PacketType = iota
	CLOSE
	PING
	PONG
	MESSAGE
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
	}
	return fmt.Sprintf("PacketType(%d)", (int8)(t))
}

func (t PacketType) ID() string {
	switch t {
	case OPEN:
		return "0"
	case CLOSE:
		return "1"
	case PING:
		return "2"
	case PONG:
		return "3"
	case MESSAGE:
		return "4"
	}
	panic(fmt.Errorf("Unknown engine.io packet type %d", t))
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
	}
	return unknownType
}
