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

package utils

import (
	"sync"
)

type EventCallback[A any, B any] struct {
	once bool
	cb   func(A, B)
}

type HandlerList[A any, B any] struct {
	mux       sync.Mutex
	callbacks []*EventCallback[A, B]
}

func (l *HandlerList[A, B]) On(cb func(A, B)) (cancel func()) {
	ecb := &EventCallback[A, B]{
		once: false,
		cb:   cb,
	}
	canceled := false
	cancel = func() {
		if canceled {
			return
		}
		l.mux.Lock()
		defer l.mux.Unlock()
		for i, c := range l.callbacks {
			if ecb == c {
				n := len(l.callbacks) - 1
				l.callbacks[i] = l.callbacks[n]
				l.callbacks = l.callbacks[:n]
				return
			}
		}
	}

	l.mux.Lock()
	defer l.mux.Unlock()
	l.callbacks = append(l.callbacks, ecb)
	return
}

func (l *HandlerList[A, B]) Once(cb func(A, B)) (cancel func()) {
	ecb := &EventCallback[A, B]{
		once: true,
		cb:   cb,
	}
	canceled := false
	cancel = func() {
		if canceled {
			return
		}
		l.mux.Lock()
		defer l.mux.Unlock()
		for i, c := range l.callbacks {
			if ecb == c {
				n := len(l.callbacks) - 1
				l.callbacks[i] = l.callbacks[n]
				l.callbacks = l.callbacks[:n]
				return
			}
		}
	}

	l.mux.Lock()
	defer l.mux.Unlock()
	l.callbacks = append(l.callbacks, ecb)
	return
}

func (l *HandlerList[A, B]) Call(a A, b B) {
	l.mux.Lock()
	defer l.mux.Unlock()

	for i := 0; i < len(l.callbacks); {
		e := l.callbacks[i]
		e.cb(a, b)
		if e.once {
			n := len(l.callbacks) - 1
			l.callbacks[i] = l.callbacks[n]
			l.callbacks = l.callbacks[:n]
		} else {
			i++
		}
	}
}
