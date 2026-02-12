// Copyright 2024 the libevm authors.
//
// The libevm additions to go-ethereum are free software: you can redistribute
// them and/or modify them under the terms of the GNU Lesser General Public License
// as published by the Free Software Foundation, either version 3 of the License,
// or (at your option) any later version.
//
// The libevm additions are distributed in the hope that they will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU Lesser
// General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the go-ethereum library. If not, see
// <http://www.gnu.org/licenses/>.

package state

import (
	"github.com/ava-labs/libevm/common"
	"github.com/ava-labs/libevm/core/types"
	"github.com/ava-labs/libevm/internal/libevm/pseudo"
)

type StateDBExt interface {
	GetExtra(addr common.Address) *types.StateAccountExtra
	SetExtra(addr common.Address, extra *types.StateAccountExtra)
}

// GetExtra returns the extra payload from the [types.StateAccount] associated
// with the address, or a zero-value `SA` if not found. The [pseudo.Accessor]
// MUST be sourced from [types.RegisterExtras].
func GetExtra[SA any](s StateDBExt, a pseudo.Accessor[**types.StateAccountExtra, SA], addr common.Address) SA {
	if extra := s.GetExtra(addr); extra != nil {
		return a.Get(&extra)
	}
	var zero SA
	return zero
}

// SetExtra sets the extra payload for the address. See [GetExtra] for details.
func SetExtra[SA any](s StateDBExt, a pseudo.Accessor[**types.StateAccountExtra, SA], addr common.Address, extra SA) {
	current := s.GetExtra(addr)
	var next *types.StateAccountExtra
	if current != nil {
		cpy := *current
		next = &cpy
	}
	a.Set(&next, extra)
	s.SetExtra(addr, next)
}
