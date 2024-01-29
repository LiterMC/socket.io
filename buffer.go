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
	"reflect"
)

var errNotPlaceHolder = errors.New("socket.io: require `{ _placeholder: true, num: <number> }` in order to unmarshal socket.Buffer")

type Buffer struct {
	B   []byte
	num uint
}

var (
	_ json.Marshaler   = Buffer{}
	_ json.Unmarshaler = (*Buffer)(nil)
)

type placeholder struct {
	Placeholder bool `json:"_placeholder"`
	Num         uint `json:"num"`
}

func (b Buffer) MarshalJSON() ([]byte, error) {
	return json.Marshal(placeholder{
		Placeholder: true,
		Num:         b.num - 1,
	})
}

func (b *Buffer) UnmarshalJSON(data []byte) (err error) {
	var p placeholder
	if err = json.Unmarshal(data, &p); err != nil {
		return errNotPlaceHolder
	}
	if !p.Placeholder { // is null
		b.B = nil
		b.num = 0
	} else {
		b.num = p.Num + 1
	}
	return
}

var bufferTyp = reflect.TypeOf((*Buffer)(nil)).Elem()

func needCheckForBufferType(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Array, reflect.Slice, reflect.Map, reflect.Pointer:
		return needCheckForBufferType(t.Elem())
	case reflect.Struct, reflect.Interface:
		return true
	}
	return false
}

func (p *Packet) encodeAttachs(v []any) {
	p._encodeAttachs(reflect.ValueOf(v), false, make(map[*Buffer]struct{}))
}

func (p *Packet) _encodeAttachs(v reflect.Value, recurse bool, encoded map[*Buffer]struct{}) {
	typ := v.Type()
	switch typ.Kind() {
	case reflect.Array:
		if et := typ.Elem(); recurse || needCheckForBufferType(et) {
			recurse = et.Kind() != reflect.Interface
			l := typ.Len()
			for i := 0; i < l; i++ {
				p._encodeAttachs(v.Index(i), recurse, encoded)
			}
		}
	case reflect.Slice:
		if et := typ.Elem(); recurse || needCheckForBufferType(et) {
			recurse = et.Kind() != reflect.Interface
			l := v.Len()
			for i := 0; i < l; i++ {
				p._encodeAttachs(v.Index(i), recurse, encoded)
			}
		}
	case reflect.Map:
		if et := typ.Elem(); recurse || needCheckForBufferType(et) {
			recurse = et.Kind() != reflect.Interface
			iter := v.MapRange()
			for iter.Next() {
				p._encodeAttachs(iter.Value(), recurse, encoded)
			}
		}
	case reflect.Pointer:
		if et := typ.Elem(); (recurse || needCheckForBufferType(et)) && !v.IsNil() {
			recurse = et.Kind() != reflect.Interface
			p._encodeAttachs(v.Elem(), recurse, encoded)
		}
	case reflect.Struct:
		if typ == bufferTyp {
			b := v.Addr().Interface().(*Buffer)
			if _, ok := encoded[b]; !ok {
				encoded[b] = struct{}{}
				p.attachs = append(p.attachs, b.B)
				b.num = (uint)(len(p.attachs))
			}
			return
		}
		l := typ.NumField()
		for i := 0; i < l; i++ {
			p._encodeAttachs(v.Field(i), false, encoded)
		}
	}
}

func (p *Packet) decodeAttachs(ptr any) {
	p._decodeAttachs(reflect.ValueOf(ptr), false)
}

func (p *Packet) _decodeAttachs(v reflect.Value, recurse bool) {
	typ := v.Type()
	switch typ.Kind() {
	case reflect.Array:
		if et := typ.Elem(); recurse || needCheckForBufferType(et) {
			recurse = et.Kind() != reflect.Interface
			l := typ.Len()
			for i := 0; i < l; i++ {
				p._decodeAttachs(v.Index(i), recurse)
			}
		}
	case reflect.Slice:
		if et := typ.Elem(); recurse || needCheckForBufferType(et) {
			recurse = et.Kind() != reflect.Interface
			l := v.Len()
			for i := 0; i < l; i++ {
				p._decodeAttachs(v.Index(i), recurse)
			}
		}
	case reflect.Map:
		if et := typ.Elem(); recurse || needCheckForBufferType(et) {
			recurse = et.Kind() != reflect.Interface
			iter := v.MapRange()
			for iter.Next() {
				p._decodeAttachs(iter.Value(), recurse)
			}
		}
	case reflect.Pointer:
		if et := typ.Elem(); (recurse || needCheckForBufferType(et)) && !v.IsNil() {
			recurse = et.Kind() != reflect.Interface
			p._decodeAttachs(v.Elem(), recurse)
		}
	case reflect.Struct:
		if typ == bufferTyp {
			b := v.Addr().Interface().(*Buffer)
			if b.num > 0 {
				b.B = p.attachs[b.num-1]
			}
			return
		}
		l := typ.NumField()
		for i := 0; i < l; i++ {
			p._decodeAttachs(v.Field(i), false)
		}
	}
}
