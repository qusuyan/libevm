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
// along with the go-ethereum library. If not, see
// <http://www.gnu.org/licenses/>.

package vm

import (
	"sync/atomic"

	"github.com/holiman/uint256"

	"github.com/ava-labs/libevm/common"
	"github.com/ava-labs/libevm/libevm"
	"github.com/ava-labs/libevm/log"
)

// canCreateContract is a convenience wrapper for calling the
// [params.RulesHooks.CanCreateContract] hook.
//
// Hooks are expected to return a remaining gas value that does not exceed the
// input gas amount.
func (evm *EVM) canCreateContract(caller ContractRef, contractToCreate common.Address, gas uint64) (remainingGas uint64, _ error) {
	addrs := &libevm.AddressContext{
		Origin: evm.Origin,
		EVMSemantic: libevm.CallerAndSelf{
			Caller: caller.Address(),
			Self:   contractToCreate,
		},
		// The "raw" caller isn't guaranteed to be known if the caller is a
		// delegate so the `Raw` field is documented as always being nil.
	}
	gas, err := evm.chainRules.Hooks().CanCreateContract(addrs, gas, evm.StateDB)

	// NOTE that this block only performs logging and that all paths propagate
	// `(gas, err)` unmodified.
	if err != nil {
		log.Debug(
			"Contract creation blocked by libevm hook",
			"origin", addrs.Origin,
			"caller", addrs.EVMSemantic.Caller,
			"contract", addrs.EVMSemantic.Self,
			"hooks", log.TypeOf(evm.chainRules.Hooks()),
			"reason", err,
		)
	}

	return gas, err
}

// Call executes the contract associated with the addr with the given input as
// parameters. It also handles any necessary value transfer required and takes
// the necessary steps to create accounts and reverses the state in case of an
// execution error or failed value transfer.
func (evm *EVM) Call(caller ContractRef, addr common.Address, input []byte, gas uint64, value *uint256.Int) (ret []byte, leftOverGas uint64, err error) {
	gas, err = evm.spendPreprocessingGas(gas)
	if err != nil {
		return nil, gas, err
	}
	if endTracking := evm.beginTopLevelGasTrackingIfRoot(gas); endTracking != nil {
		defer endTracking()
	}
	return evm.call(caller, addr, input, gas, value)
}

// create wraps the original geth method of the same name, now named
// [EVM.createCommon], first spending preprocessing gas.
func (evm *EVM) create(caller ContractRef, codeAndHash *codeAndHash, gas uint64, value *uint256.Int, address common.Address, typ OpCode) (ret []byte, addr common.Address, leftOverGas uint64, err error) {
	gas, err = evm.spendPreprocessingGas(gas)
	if err != nil {
		return nil, common.Address{}, gas, err
	}
	if endTracking := evm.beginTopLevelGasTrackingIfRoot(gas); endTracking != nil {
		defer endTracking()
	}
	return evm.createCommon(caller, codeAndHash, gas, value, address, typ)
}

func (evm *EVM) spendPreprocessingGas(gas uint64) (uint64, error) {
	if internalCall := evm.depth > 0; internalCall || !libevmHooks.Registered() {
		return gas, nil
	}
	c, err := libevmHooks.Get().PreprocessingGasCharge(evm.StateDB.TxHash())
	if err != nil {
		return gas, err
	}
	if c > gas {
		return 0, ErrOutOfGas
	}
	return gas - c, nil
}

// InvalidateExecution sets the error that will be returned by
// [EVM.ExecutionInvalidated] for the length of the current transaction; i.e.
// until [EVM.Reset] is called. This is honoured by state-transition logic to
// render the execution itself void (as against reverted).
//
// This method MUST NOT be exposed in a manner that allows contracts to set
// the error; it MAY be exposed to precompiles.
func (evm *EVM) InvalidateExecution(err error) {
	evm.executionInvalidated = err
}

// ExecutionInvalidated returns the last value passed to
// [EVM.InvalidateExecution] or nil if no such call has occurred or if
// [EVM.Reset] has been called.
func (evm *EVM) ExecutionInvalidated() error {
	return evm.executionInvalidated
}

func (evm *EVM) beginTopLevelGasTracking(_ uint64) {
	evm.forwardedCallGas = 0
}

func (evm *EVM) endTopLevelGasTracking() {
	evm.forwardedCallGas = 0
	// Drain the worker-local gas batch so post-run reads of the shared counter
	// (e.g. the scheduler's execution-completion position) observe the exact total.
	evm.topLevelGasConsumed.flush()
}

func (evm *EVM) resetTopLevelGasTrackingWindow() {
	evm.forwardedCallGas = 0
	evm.topLevelGasConsumed.flush()
}

// TopLevelGasConsumed returns the cumulative monotonic post-preprocessing gas
// consumed across all top-level runs executed by this EVM. This includes gas
// consumed by nested calls and persists across Reset().
//
// It returns the shared (cross-worker-visible) counter, which a worker's own
// in-flight UseGas batches into only at the worker-local flush threshold and at
// the end of each top-level run. Reads performed BETWEEN top-level runs (the
// only time a peer scheduler samples another worker, and the only time the
// owning worker samples itself for position bookkeeping) therefore observe the
// exact total; a concurrent sampler observing a peer mid-run may lag by up to
// schedulerGasFlushThreshold gas, which is acceptable for the gas-ordering
// signal and is the point of the batching (see topLevelGasTracker).
func (evm *EVM) TopLevelGasConsumed() uint64 {
	return evm.topLevelGasConsumed.load()
}

func (evm *EVM) ConsumeTopLevelGas(gas uint64) {
	// Validation/commit charges happen outside the hot per-opcode loop, so add
	// them straight to the shared counter (flushing any pending local first).
	evm.topLevelGasConsumed.addShared(gas)
}

const (
	// schedulerGasFlushThreshold is the worker-local gas a worker accumulates
	// before flushing it into the shared, cross-worker-visible counter. Batching
	// the high-frequency per-opcode updates this way cuts the cache-coherence
	// traffic created by schedulers that spin-read the shared counter.
	schedulerGasFlushThreshold = 2000
	cacheLineBytes             = 64
)

// topLevelGasTracker accumulates a worker EVM's cumulative scheduler-accounting
// gas. `shared` is sampled concurrently by OTHER workers' schedulers (the
// gas-ordering signal); `local` is private to the owning worker's goroutine and
// flushed into `shared` in batches — every schedulerGasFlushThreshold gas and at
// the end of each top-level run. `shared` is isolated on its own cache line by
// the surrounding padding so the owning worker's frequent `local` writes (and
// any neighbouring EVM field) never invalidate the line that peer workers read.
type topLevelGasTracker struct {
	_      [cacheLineBytes]byte
	shared atomic.Uint64
	_      [cacheLineBytes - 8]byte
	local  uint64
}

// add accumulates worker-local gas, flushing to the shared counter only once the
// local batch reaches the threshold. Called from the hot per-opcode path.
func (t *topLevelGasTracker) add(gas uint64) {
	if gas == 0 {
		return
	}
	t.local += gas
	if t.local >= schedulerGasFlushThreshold {
		t.shared.Add(t.local)
		t.local = 0
	}
}

// addShared flushes any pending local batch then adds directly to the shared
// counter. For infrequent charges that occur outside the hot loop.
func (t *topLevelGasTracker) addShared(gas uint64) {
	t.flush()
	if gas != 0 {
		t.shared.Add(gas)
	}
}

// flush drains the worker-local batch into the shared counter. Called at the end
// of each top-level run so post-run reads of the shared counter are exact.
func (t *topLevelGasTracker) flush() {
	if t.local != 0 {
		t.shared.Add(t.local)
		t.local = 0
	}
}

func (t *topLevelGasTracker) load() uint64 {
	return t.shared.Load()
}

// ValueTransferCallStipendCount reports how many times a value-transferring
// CALL / CALLCODE has injected params.CallStipend (2300 free gas) into a child
// frame. The stipend is added to the child's local gas pool, so the child's
// UseGas calls inflate TopLevelGasConsumed by up to 2300 per stipend even
// though the parent never pays the stipend. Callers that need to reconcile
// TopLevelGasConsumed against receipt.GasUsed (e.g. policy schedulers) can use
// this counter to compute the inflation.
func (evm *EVM) ValueTransferCallStipendCount() uint64 {
	return evm.valueTransferCallStipendCount
}
