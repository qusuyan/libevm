// Copyright 2025 the libevm authors.
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
// along with the libevm library. If not, see <http://www.gnu.org/licenses/>.

package vm

import (
	"fmt"

	"github.com/ava-labs/libevm/params"
)

// LookupSchedulerAccountingCost returns the total scheduler-accounting cost for
// the given opcode under the supplied chain rules:
//
//	constantGas + schedulerAccountingGas
//
// The result is a state-independent approximation of the opcode's cost, suitable
// for use as a parallel-execution scheduling signal during validation and commit.
func LookupSchedulerAccountingCost(rules params.Rules, op OpCode) (uint64, error) {
	jt := instructionSetForRules(rules)
	entry := jt[op]
	if entry == nil {
		return 0, fmt.Errorf("opcode %v not present in instruction set for given rules", op)
	}
	if entry.schedulerAccountingGas == 0 {
		return entry.constantGas, nil
	}
	return entry.schedulerAccountingGas, nil
}

// SchedulerKeccak256MemoryCost returns the KECCAK256 memory-expansion cost for
// a payload of byteLen bytes. This is the "memory cost only" portion of the
// KECCAK256 gas formula; the per-word hashing charge is intentionally excluded.
//
// Used by parallel executors to charge scheduler-accounting gas for preimage
// insertions during commit, where the exact hashing work is already done but we
// want to record bounded bookkeeping cost.
func SchedulerKeccak256MemoryCost(byteLen int) uint64 {
	if byteLen <= 0 {
		return 0
	}
	words := uint64((byteLen + 31) / 32)
	return words * params.Keccak256WordGas
}

// instructionSetForRules returns a pointer to the package-level JumpTable that
// corresponds to the highest fork active in rules. The selection order mirrors
// NewEVMInterpreter so that scheduler accounting values match actual opcode
// pricing for the same fork.
func instructionSetForRules(rules params.Rules) *JumpTable {
	switch {
	case rules.IsCancun:
		return &cancunInstructionSet
	case rules.IsShanghai:
		return &shanghaiInstructionSet
	case rules.IsMerge:
		return &mergeInstructionSet
	case rules.IsLondon:
		return &londonInstructionSet
	case rules.IsBerlin:
		return &berlinInstructionSet
	case rules.IsIstanbul:
		return &istanbulInstructionSet
	case rules.IsConstantinople:
		return &constantinopleInstructionSet
	case rules.IsByzantium:
		return &byzantiumInstructionSet
	case rules.IsEIP158:
		return &spuriousDragonInstructionSet
	case rules.IsEIP150:
		return &tangerineWhistleInstructionSet
	case rules.IsHomestead:
		return &homesteadInstructionSet
	default:
		return &frontierInstructionSet
	}
}
