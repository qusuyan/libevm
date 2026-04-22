// Copyright 2024-2025 the libevm authors.
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
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ava-labs/libevm/common"
	"github.com/ava-labs/libevm/core/rawdb"
	"github.com/ava-labs/libevm/core/state"
	"github.com/ava-labs/libevm/core/types"
	"github.com/ava-labs/libevm/crypto"
	"github.com/ava-labs/libevm/libevm"
	"github.com/ava-labs/libevm/params"
	"github.com/holiman/uint256"
)

type evmArgOverrider struct {
	newEVMchainID int64

	gotResetChainID  *big.Int
	resetTxContextTo TxContext
	resetStateDBTo   StateDB
	preprocessingGas uint64
	canCreateSpend   uint64
}

func (o *evmArgOverrider) OverrideNewEVMArgs(args *NewEVMArgs) *NewEVMArgs {
	args.ChainConfig = &params.ChainConfig{ChainID: big.NewInt(o.newEVMchainID)}
	return args
}

func (o *evmArgOverrider) OverrideEVMResetArgs(r params.Rules, _ *EVMResetArgs) *EVMResetArgs {
	o.gotResetChainID = r.ChainID
	return &EVMResetArgs{
		TxContext: o.resetTxContextTo,
		StateDB:   o.resetStateDBTo,
	}
}

func (*evmArgOverrider) PreprocessingGasCharge(common.Hash) (uint64, error) {
	return 0, nil
}

func (o *evmArgOverrider) CanCreateContract(*libevm.AddressContext, uint64, libevm.StateReader) (uint64, error) {
	if o.canCreateSpend == 0 {
		return 0, nil
	}
	panic("CanCreateContract should be overridden per test")
}

type topLevelGasHooks struct {
	preprocessingGas uint64
	canCreateSpend   uint64
}

func (h *topLevelGasHooks) OverrideNewEVMArgs(args *NewEVMArgs) *NewEVMArgs { return args }
func (h *topLevelGasHooks) OverrideEVMResetArgs(_ params.Rules, args *EVMResetArgs) *EVMResetArgs {
	return args
}
func (h *topLevelGasHooks) PreprocessingGasCharge(common.Hash) (uint64, error) {
	return h.preprocessingGas, nil
}
func (h *topLevelGasHooks) CanCreateContract(_ *libevm.AddressContext, gas uint64, _ libevm.StateReader) (uint64, error) {
	if h.canCreateSpend > gas {
		return 0, ErrOutOfGas
	}
	return gas - h.canCreateSpend, nil
}

func (h *topLevelGasHooks) register(t *testing.T) {
	t.Helper()
	TestOnlyClearRegisteredHooks()
	RegisterHooks(h)
	t.Cleanup(TestOnlyClearRegisteredHooks)
}

func (o *evmArgOverrider) register(t *testing.T) {
	t.Helper()
	TestOnlyClearRegisteredHooks()
	RegisterHooks(o)
	t.Cleanup(TestOnlyClearRegisteredHooks)
}

func TestOverrideNewEVMArgs(t *testing.T) {
	// The overrideNewEVMArgs function accepts and returns all arguments to
	// NewEVM(), in order. Here we lock in our assumption of that order. If this
	// breaks then all functionality overriding the args MUST be updated.
	var _ func(BlockContext, TxContext, StateDB, *params.ChainConfig, Config) *EVM = NewEVM

	const chainID = 13579
	hooks := evmArgOverrider{newEVMchainID: chainID}
	hooks.register(t)

	assertChainID := func(t *testing.T, want int64) {
		t.Helper()
		evm := NewEVM(BlockContext{}, TxContext{}, nil, nil, Config{})
		got := evm.ChainConfig().ChainID
		require.Equalf(t, big.NewInt(want), got, "%T.ChainConfig().ChainID set by NewEVM() hook", evm)
	}
	assertChainID(t, chainID)

	t.Run("WithTempRegisteredHooks", func(t *testing.T) {
		err := libevm.WithTemporaryExtrasLock(func(lock libevm.ExtrasLock) error {
			override := evmArgOverrider{newEVMchainID: 24680}
			return WithTempRegisteredHooks(lock, &override, func() error {
				assertChainID(t, override.newEVMchainID)
				return nil
			})
		})
		require.NoError(t, err)
		t.Run("after", func(t *testing.T) {
			assertChainID(t, chainID)
		})
	})
}

func TestOverrideEVMResetArgs(t *testing.T) {
	// Equivalent to rationale for TestOverrideNewEVMArgs above.
	var _ func(TxContext, StateDB) = (*EVM)(nil).Reset

	const (
		chainID  = 0xc0ffee
		gasPrice = 1357924680
	)
	hooks := &evmArgOverrider{
		newEVMchainID: chainID,
		resetTxContextTo: TxContext{
			GasPrice: big.NewInt(gasPrice),
		},
	}
	hooks.register(t)

	evm := NewEVM(BlockContext{}, TxContext{}, nil, nil, Config{})
	evm.Reset(TxContext{}, nil)
	assert.Equalf(t, big.NewInt(chainID), hooks.gotResetChainID, "%T.ChainID passed to Reset() hook", params.Rules{})
	assert.Equalf(t, big.NewInt(gasPrice), evm.GasPrice, "%T.GasPrice set by Reset() hook", evm)
}

type topLevelGasTracer struct {
	evm      *EVM
	consumed []uint64
	depths   []int
}

func (t *topLevelGasTracer) CaptureTxStart(uint64) {}
func (t *topLevelGasTracer) CaptureTxEnd(uint64)   {}

func (t *topLevelGasTracer) CaptureStart(env *EVM, _ common.Address, _ common.Address, _ bool, _ []byte, _ uint64, _ *big.Int) {
	t.evm = env
}

func (t *topLevelGasTracer) CaptureEnd(_ []byte, _ uint64, _ error) {}
func (t *topLevelGasTracer) CaptureEnter(_ OpCode, _ common.Address, _ common.Address, _ []byte, _ uint64, _ *big.Int) {
}
func (t *topLevelGasTracer) CaptureExit(_ []byte, _ uint64, _ error) {}

func (t *topLevelGasTracer) CaptureState(_ uint64, _ OpCode, _ uint64, _ uint64, _ *ScopeContext, _ []byte, _ int, _ error) {
	if t.evm == nil {
		return
	}
	t.consumed = append(t.consumed, t.evm.TopLevelGasConsumed())
	t.depths = append(t.depths, t.evm.depth)
}

func (t *topLevelGasTracer) CaptureFault(_ uint64, _ OpCode, _ uint64, _ uint64, _ *ScopeContext, _ int, _ error) {
}

func newTestEVMWithCode(t *testing.T, code []byte, tracer EVMLogger) (*state.StateDB, *EVM, common.Address, common.Address) {
	t.Helper()

	statedb, err := state.New(types.EmptyRootHash, state.NewDatabase(rawdb.NewMemoryDatabase()), nil)
	require.NoError(t, err)

	caller := common.HexToAddress("0xca11")
	contractAddr := common.HexToAddress("0xc0de")
	statedb.CreateAccount(caller)
	statedb.CreateAccount(contractAddr)
	statedb.SetBalance(caller, uint256.NewInt(1_000_000_000))
	statedb.SetCode(contractAddr, code)

	evm := NewEVM(
		BlockContext{
			CanTransfer: func(db StateDB, addr common.Address, amount *uint256.Int) bool {
				return db.GetBalance(addr).Cmp(amount) >= 0
			},
			Transfer: func(db StateDB, from, to common.Address, amount *uint256.Int) {
				db.SubBalance(from, amount)
				db.AddBalance(to, amount)
			},
			BlockNumber: big.NewInt(1),
			Time:        1,
		},
		TxContext{},
		statedb,
		params.TestChainConfig,
		Config{Tracer: tracer},
	)
	return statedb, evm, caller, contractAddr
}

func mustDecodeHex(t *testing.T, code string) []byte {
	t.Helper()

	decoded, err := hex.DecodeString(code)
	require.NoError(t, err)
	return decoded
}

func assertTopLevelGasTrace(t *testing.T, tracer *topLevelGasTracer, childDepth int) {
	t.Helper()

	require.NotEmpty(t, tracer.consumed)

	childSamples := 0
	childProgress := false
	for i, consumed := range tracer.consumed {
		if i > 0 {
			require.GreaterOrEqualf(t, consumed, tracer.consumed[i-1], "expected monotonic consumed gas at sample %d", i)
		}
		if tracer.depths[i] == childDepth {
			childSamples++
			if i > 0 && consumed > tracer.consumed[i-1] {
				childProgress = true
			}
		}
	}
	require.NotZero(t, childSamples, "expected tracer samples from nested execution")
	require.True(t, childProgress, "expected accumulated consumed gas to advance during nested execution")
}

func assertTopLevelRunDelta(t *testing.T, evm *EVM, startConsumed, gasLimit, leftOverGas uint64) {
	t.Helper()

	endConsumed := evm.TopLevelGasConsumed()
	require.Equal(t, gasLimit-leftOverGas, endConsumed-startConsumed)
}

func TestTopLevelGasProgressIsMonotonicAcrossNestedGasReturn(t *testing.T) {
	child := common.HexToAddress("0x1234")
	childCode := []byte{
		0x60, 0x01, 0x50,
		0x60, 0x02, 0x50,
		0x60, 0x03, 0x50,
		0x60, 0x04, 0x50,
		0x00,
	}
	code := append(
		[]byte{
			0x60, 0x00,
			0x60, 0x00,
			0x60, 0x00,
			0x60, 0x00,
			0x60, 0x00,
			0x73,
		},
		child.Bytes()...,
	)
	code = append(code,
		0x61, 0x0f, 0xff,
		0xf1,
		0x00,
	)

	tracer := &topLevelGasTracer{}
	statedb, evm, caller, contractAddr := newTestEVMWithCode(t, code, tracer)
	statedb.CreateAccount(child)
	statedb.SetCode(child, childCode)

	const gasLimit = uint64(100_000)
	startConsumed := evm.TopLevelGasConsumed()
	_, leftOverGas, err := evm.Call(AccountRef(caller), contractAddr, nil, gasLimit, uint256.NewInt(0))
	require.NoError(t, err)

	assertTopLevelGasTrace(t, tracer, 2)
	assertTopLevelRunDelta(t, evm, startConsumed, gasLimit, leftOverGas)
}

func TestTopLevelGasProgressDuringStaticCall(t *testing.T) {
	child := common.HexToAddress("0x2345")
	childCode := []byte{
		0x60, 0x01, 0x50,
		0x60, 0x02, 0x50,
		0x60, 0x03, 0x50,
		0x60, 0x04, 0x50,
		0x00,
	}
	code := append(
		[]byte{
			0x60, 0x00,
			0x60, 0x00,
			0x60, 0x00,
			0x60, 0x00,
			0x73,
		},
		child.Bytes()...,
	)
	code = append(code,
		0x61, 0x0f, 0xff,
		0xfa,
		0x00,
	)

	tracer := &topLevelGasTracer{}
	statedb, evm, caller, contractAddr := newTestEVMWithCode(t, code, tracer)
	statedb.CreateAccount(child)
	statedb.SetCode(child, childCode)

	const gasLimit = uint64(100_000)
	startConsumed := evm.TopLevelGasConsumed()
	_, leftOverGas, err := evm.Call(AccountRef(caller), contractAddr, nil, gasLimit, uint256.NewInt(0))
	require.NoError(t, err)

	assertTopLevelGasTrace(t, tracer, 2)
	assertTopLevelRunDelta(t, evm, startConsumed, gasLimit, leftOverGas)
}

func TestTopLevelGasProgressForDirectStaticCall(t *testing.T) {
	code := []byte{0x60, 0x01, 0x50, 0x00}
	_, evm, caller, contractAddr := newTestEVMWithCode(t, code, nil)

	const gasLimit = uint64(50_000)
	startConsumed := evm.TopLevelGasConsumed()
	_, leftOverGas, err := evm.StaticCall(AccountRef(caller), contractAddr, nil, gasLimit)
	require.NoError(t, err)

	assertTopLevelRunDelta(t, evm, startConsumed, gasLimit, leftOverGas)
}

func TestTopLevelGasProgressForDirectCallCode(t *testing.T) {
	code := []byte{0x60, 0x01, 0x50, 0x00}
	_, evm, caller, contractAddr := newTestEVMWithCode(t, code, nil)

	const gasLimit = uint64(50_000)
	startConsumed := evm.TopLevelGasConsumed()
	_, leftOverGas, err := evm.CallCode(AccountRef(caller), contractAddr, nil, gasLimit, uint256.NewInt(0))
	require.NoError(t, err)

	assertTopLevelRunDelta(t, evm, startConsumed, gasLimit, leftOverGas)
}

func TestTopLevelGasProgressForDirectDelegateCall(t *testing.T) {
	code := []byte{0x60, 0x01, 0x50, 0x00}
	_, evm, caller, contractAddr := newTestEVMWithCode(t, code, nil)
	callerContract := NewContract(AccountRef(caller), AccountRef(caller), uint256.NewInt(0), 50_000)

	startConsumed := evm.TopLevelGasConsumed()
	_, leftOverGas, err := evm.DelegateCall(callerContract, contractAddr, nil, 50_000)
	require.NoError(t, err)

	assertTopLevelRunDelta(t, evm, startConsumed, 50_000, leftOverGas)
}

func TestTopLevelGasProgressDuringCreate(t *testing.T) {
	initCode := mustDecodeHex(t, "60015060025060006000f3")
	initOffset := byte(15)
	parentCode := append(
		[]byte{
			0x60, byte(len(initCode)),
			0x60, initOffset,
			0x60, 0x00,
			0x39,
			0x60, byte(len(initCode)),
			0x60, 0x00,
			0x60, 0x00,
			0xf0,
			0x00,
		},
		initCode...,
	)

	tracer := &topLevelGasTracer{}
	_, evm, caller, contractAddr := newTestEVMWithCode(t, parentCode, tracer)

	const gasLimit = uint64(150_000)
	startConsumed := evm.TopLevelGasConsumed()
	_, leftOverGas, err := evm.Call(AccountRef(caller), contractAddr, nil, gasLimit, uint256.NewInt(0))
	require.NoError(t, err)

	assertTopLevelGasTrace(t, tracer, 2)
	assertTopLevelRunDelta(t, evm, startConsumed, gasLimit, leftOverGas)
}

func TestTopLevelGasProgressForDirectCreate(t *testing.T) {
	initCode := mustDecodeHex(t, "60015060025060006000f3")
	_, evm, caller, _ := newTestEVMWithCode(t, nil, nil)

	const gasLimit = uint64(150_000)
	startConsumed := evm.TopLevelGasConsumed()
	_, _, leftOverGas, err := evm.Create(AccountRef(caller), initCode, gasLimit, uint256.NewInt(0))
	require.NoError(t, err)

	assertTopLevelRunDelta(t, evm, startConsumed, gasLimit, leftOverGas)
}

func TestTopLevelGasTrackingResetPreservesCumulativeConsumption(t *testing.T) {
	code := []byte{0x60, 0x00, 0x50, 0x00}
	_, evm, caller, contractAddr := newTestEVMWithCode(t, code, nil)

	startConsumed := evm.TopLevelGasConsumed()
	_, leftOverGas, err := evm.Call(AccountRef(caller), contractAddr, nil, 50_000, uint256.NewInt(0))
	require.NoError(t, err)

	assertTopLevelRunDelta(t, evm, startConsumed, 50_000, leftOverGas)

	cumulative := evm.TopLevelGasConsumed()
	evm.Reset(TxContext{}, evm.StateDB)

	require.Equal(t, cumulative, evm.TopLevelGasConsumed())
}

func TestTopLevelGasConsumedAccumulatesAcrossRuns(t *testing.T) {
	code := []byte{0x60, 0x00, 0x50, 0x00}
	_, evm, caller, contractAddr := newTestEVMWithCode(t, code, nil)

	startConsumed := evm.TopLevelGasConsumed()
	_, leftOverGas, err := evm.Call(AccountRef(caller), contractAddr, nil, 50_000, uint256.NewInt(0))
	require.NoError(t, err)
	firstDelta := 50_000 - leftOverGas
	require.Equal(t, firstDelta, evm.TopLevelGasConsumed()-startConsumed)

	evm.Reset(TxContext{}, evm.StateDB)

	afterReset := evm.TopLevelGasConsumed()
	_, leftOverGas, err = evm.Call(AccountRef(caller), contractAddr, nil, 60_000, uint256.NewInt(0))
	require.NoError(t, err)
	secondDelta := uint64(60_000) - leftOverGas
	require.Equal(t, secondDelta, evm.TopLevelGasConsumed()-afterReset)
	require.Equal(t, startConsumed+firstDelta+secondDelta, evm.TopLevelGasConsumed())
}

func TestTopLevelGasConsumedMatchesMultipleRunsEndToEnd(t *testing.T) {
	rootCode := []byte{0x60, 0x01, 0x50, 0x00}
	statedb, evm, caller, rootAddr := newTestEVMWithCode(t, rootCode, nil)

	staticAddr := common.HexToAddress("0x1234")
	revertAddr := common.HexToAddress("0x2345")
	statedb.CreateAccount(staticAddr)
	statedb.CreateAccount(revertAddr)
	statedb.SetCode(staticAddr, []byte{0x60, 0x02, 0x50, 0x00})
	statedb.SetCode(revertAddr, []byte{0x60, 0x00, 0x60, 0x00, 0xfd})

	initialConsumed := evm.TopLevelGasConsumed()
	var expectedTotal uint64

	{
		startConsumed := evm.TopLevelGasConsumed()
		const gasLimit = uint64(50_000)
		_, leftOverGas, err := evm.Call(AccountRef(caller), rootAddr, nil, gasLimit, uint256.NewInt(0))
		require.NoError(t, err)
		runConsumed := gasLimit - leftOverGas
		expectedTotal += runConsumed
		require.Equal(t, runConsumed, evm.TopLevelGasConsumed()-startConsumed)
	}

	{
		startConsumed := evm.TopLevelGasConsumed()
		const gasLimit = uint64(60_000)
		_, leftOverGas, err := evm.StaticCall(AccountRef(caller), staticAddr, nil, gasLimit)
		require.NoError(t, err)
		runConsumed := gasLimit - leftOverGas
		expectedTotal += runConsumed
		require.Equal(t, runConsumed, evm.TopLevelGasConsumed()-startConsumed)
	}

	{
		startConsumed := evm.TopLevelGasConsumed()
		const gasLimit = uint64(70_000)
		_, leftOverGas, err := evm.Call(AccountRef(caller), common.BytesToAddress([]byte{2}), []byte{0x01, 0x02, 0x03, 0x04}, gasLimit, uint256.NewInt(0))
		require.NoError(t, err)
		runConsumed := gasLimit - leftOverGas
		expectedTotal += runConsumed
		require.Equal(t, runConsumed, evm.TopLevelGasConsumed()-startConsumed)
	}

	{
		startConsumed := evm.TopLevelGasConsumed()
		initCode := mustDecodeHex(t, "60015060025060006000f3")
		const gasLimit = uint64(150_000)
		_, _, leftOverGas, err := evm.Create(AccountRef(caller), initCode, gasLimit, uint256.NewInt(0))
		require.NoError(t, err)
		runConsumed := gasLimit - leftOverGas
		expectedTotal += runConsumed
		require.Equal(t, runConsumed, evm.TopLevelGasConsumed()-startConsumed)
	}

	{
		startConsumed := evm.TopLevelGasConsumed()
		const gasLimit = uint64(80_000)
		_, leftOverGas, err := evm.Call(AccountRef(caller), revertAddr, nil, gasLimit, uint256.NewInt(0))
		require.ErrorIs(t, err, ErrExecutionReverted)
		runConsumed := gasLimit - leftOverGas
		expectedTotal += runConsumed
		require.Equal(t, runConsumed, evm.TopLevelGasConsumed()-startConsumed)
	}

	require.Equal(t, expectedTotal, evm.TopLevelGasConsumed()-initialConsumed)
}

func TestTopLevelGasConsumedMatchesHookAdjustedRunsEndToEnd(t *testing.T) {
	hooks := &topLevelGasHooks{
		preprocessingGas: 1111,
	}
	hooks.register(t)

	rootCode := []byte{0x60, 0x01, 0x50, 0x00}
	_, evm, caller, rootAddr := newTestEVMWithCode(t, rootCode, nil)

	initialConsumed := evm.TopLevelGasConsumed()
	var expectedTotal uint64

	{
		startConsumed := evm.TopLevelGasConsumed()
		const gasLimit = uint64(50_000)
		_, leftOverGas, err := evm.Call(AccountRef(caller), rootAddr, nil, gasLimit, uint256.NewInt(0))
		require.NoError(t, err)
		runLimit := gasLimit - hooks.preprocessingGas
		runConsumed := runLimit - leftOverGas
		expectedTotal += runConsumed
		require.Equal(t, runConsumed, evm.TopLevelGasConsumed()-startConsumed)
	}

	{
		startConsumed := evm.TopLevelGasConsumed()
		initCode := mustDecodeHex(t, "60015060025060006000f3")
		const gasLimit = uint64(150_000)
		_, _, leftOverGas, err := evm.Create(AccountRef(caller), initCode, gasLimit, uint256.NewInt(0))
		require.NoError(t, err)
		runLimit := gasLimit - hooks.preprocessingGas
		runConsumed := runLimit - leftOverGas
		expectedTotal += runConsumed
		require.Equal(t, runConsumed, evm.TopLevelGasConsumed()-startConsumed)
	}

	evm.Reset(TxContext{}, evm.StateDB)

	{
		startConsumed := evm.TopLevelGasConsumed()
		const gasLimit = uint64(60_000)
		_, leftOverGas, err := evm.StaticCall(AccountRef(caller), rootAddr, nil, gasLimit)
		require.NoError(t, err)
		runConsumed := gasLimit - leftOverGas
		expectedTotal += runConsumed
		require.Equal(t, runConsumed, evm.TopLevelGasConsumed()-startConsumed)
	}

	require.Equal(t, expectedTotal, evm.TopLevelGasConsumed()-initialConsumed)
}

func TestTopLevelGasProgressForRegularPrecompile(t *testing.T) {
	_, evm, caller, _ := newTestEVMWithCode(t, nil, nil)

	const gasLimit = uint64(50_000)
	startConsumed := evm.TopLevelGasConsumed()
	input := []byte{0x01, 0x02, 0x03, 0x04}
	precompileAddr := common.BytesToAddress([]byte{2})

	_, leftOverGas, err := evm.Call(AccountRef(caller), precompileAddr, input, gasLimit, uint256.NewInt(0))
	require.NoError(t, err)

	assertTopLevelRunDelta(t, evm, startConsumed, gasLimit, leftOverGas)
}

func TestTopLevelGasProgressForRegularPrecompileOutOfGas(t *testing.T) {
	_, evm, caller, _ := newTestEVMWithCode(t, nil, nil)

	const gasLimit = uint64(10)
	startConsumed := evm.TopLevelGasConsumed()
	input := []byte{0x01, 0x02, 0x03, 0x04}
	precompileAddr := common.BytesToAddress([]byte{2})

	_, leftOverGas, err := evm.Call(AccountRef(caller), precompileAddr, input, gasLimit, uint256.NewInt(0))
	require.ErrorIs(t, err, ErrOutOfGas)
	require.Zero(t, leftOverGas)

	assertTopLevelRunDelta(t, evm, startConsumed, gasLimit, leftOverGas)
}

func TestTopLevelGasProgressForTopLevelFailureConsumesAllGas(t *testing.T) {
	code := []byte{0xfe}
	_, evm, caller, contractAddr := newTestEVMWithCode(t, code, nil)

	const gasLimit = uint64(50_000)
	startConsumed := evm.TopLevelGasConsumed()
	_, leftOverGas, err := evm.Call(AccountRef(caller), contractAddr, nil, gasLimit, uint256.NewInt(0))
	require.Error(t, err)
	require.Zero(t, leftOverGas)

	assertTopLevelRunDelta(t, evm, startConsumed, gasLimit, leftOverGas)
}

func TestTopLevelGasProgressForTopLevelRevertPreservesGas(t *testing.T) {
	code := []byte{0x60, 0x00, 0x60, 0x00, 0xfd}
	_, evm, caller, contractAddr := newTestEVMWithCode(t, code, nil)

	const gasLimit = uint64(50_000)
	startConsumed := evm.TopLevelGasConsumed()
	_, leftOverGas, err := evm.Call(AccountRef(caller), contractAddr, nil, gasLimit, uint256.NewInt(0))
	require.ErrorIs(t, err, ErrExecutionReverted)
	require.NotZero(t, leftOverGas)

	assertTopLevelRunDelta(t, evm, startConsumed, gasLimit, leftOverGas)
}

func TestTopLevelGasConsumedTracksPostPreprocessingGas(t *testing.T) {
	hooks := &topLevelGasHooks{preprocessingGas: 1234}
	hooks.register(t)

	code := []byte{0x60, 0x01, 0x50, 0x00}
	_, evm, caller, contractAddr := newTestEVMWithCode(t, code, nil)

	const gasLimit = uint64(50_000)
	startConsumed := evm.TopLevelGasConsumed()
	_, leftOverGas, err := evm.Call(AccountRef(caller), contractAddr, nil, gasLimit, uint256.NewInt(0))
	require.NoError(t, err)

	assertTopLevelRunDelta(t, evm, startConsumed, gasLimit-hooks.preprocessingGas, leftOverGas)
}

func TestTopLevelGasProgressIncludesCanCreateContractCharge(t *testing.T) {
	hooks := &topLevelGasHooks{canCreateSpend: 4321}
	hooks.register(t)

	initCode := mustDecodeHex(t, "60015060025060006000f3")
	_, evm, caller, _ := newTestEVMWithCode(t, nil, nil)

	const gasLimit = uint64(150_000)
	startConsumed := evm.TopLevelGasConsumed()
	_, _, leftOverGas, err := evm.Create(AccountRef(caller), initCode, gasLimit, uint256.NewInt(0))
	require.NoError(t, err)

	assertTopLevelRunDelta(t, evm, startConsumed, gasLimit, leftOverGas)
}

func TestTopLevelGasProgressForCreateAddressCollision(t *testing.T) {
	initCode := mustDecodeHex(t, "60015060025060006000f3")
	statedb, evm, caller, _ := newTestEVMWithCode(t, nil, nil)

	collisionAddr := crypto.CreateAddress(caller, statedb.GetNonce(caller))
	statedb.CreateAccount(collisionAddr)
	statedb.SetNonce(collisionAddr, 1)

	const gasLimit = uint64(150_000)
	startConsumed := evm.TopLevelGasConsumed()
	_, _, leftOverGas, err := evm.Create(AccountRef(caller), initCode, gasLimit, uint256.NewInt(0))
	require.ErrorIs(t, err, ErrContractAddressCollision)
	require.Zero(t, leftOverGas)

	assertTopLevelRunDelta(t, evm, startConsumed, gasLimit, leftOverGas)
}

func TestTopLevelGasProgressForNestedCreateAddressCollision(t *testing.T) {
	initCode := mustDecodeHex(t, "60015060025060006000f3")
	initOffset := byte(15)
	parentCode := append(
		[]byte{
			0x60, byte(len(initCode)),
			0x60, initOffset,
			0x60, 0x00,
			0x39,
			0x60, byte(len(initCode)),
			0x60, 0x00,
			0x60, 0x00,
			0xf0,
			0x00,
		},
		initCode...,
	)

	statedb, evm, caller, contractAddr := newTestEVMWithCode(t, parentCode, nil)
	collisionAddr := crypto.CreateAddress(contractAddr, statedb.GetNonce(contractAddr))
	statedb.CreateAccount(collisionAddr)
	statedb.SetNonce(collisionAddr, 1)

	const gasLimit = uint64(150_000)
	startConsumed := evm.TopLevelGasConsumed()
	_, leftOverGas, err := evm.Call(AccountRef(caller), contractAddr, nil, gasLimit, uint256.NewInt(0))
	require.NoError(t, err)

	assertTopLevelRunDelta(t, evm, startConsumed, gasLimit, leftOverGas)
}

func TestTopLevelGasProgressForNestedCreate2AddressCollision(t *testing.T) {
	initCode := mustDecodeHex(t, "60015060025060006000f3")
	initOffset := byte(17)
	parentCode := append(
		[]byte{
			0x60, byte(len(initCode)),
			0x60, initOffset,
			0x60, 0x00,
			0x39,
			0x60, 0x2a,
			0x60, byte(len(initCode)),
			0x60, 0x00,
			0x60, 0x00,
			0xf5,
			0x00,
		},
		initCode...,
	)

	statedb, evm, caller, contractAddr := newTestEVMWithCode(t, parentCode, nil)
	collisionAddr := crypto.CreateAddress2(contractAddr, common.Hash{31: 0x2a}, crypto.Keccak256(initCode))
	statedb.CreateAccount(collisionAddr)
	statedb.SetNonce(collisionAddr, 1)

	const gasLimit = uint64(150_000)
	startConsumed := evm.TopLevelGasConsumed()
	_, leftOverGas, err := evm.Call(AccountRef(caller), contractAddr, nil, gasLimit, uint256.NewInt(0))
	require.NoError(t, err)

	assertTopLevelRunDelta(t, evm, startConsumed, gasLimit, leftOverGas)
}

func TestTopLevelGasProgressForFailedNestedCall(t *testing.T) {
	child := common.HexToAddress("0x3456")
	code := append(
		[]byte{
			0x60, 0x00,
			0x60, 0x00,
			0x60, 0x00,
			0x60, 0x00,
			0x60, 0x00,
			0x73,
		},
		child.Bytes()...,
	)
	code = append(code,
		0x61, 0x0f, 0xff,
		0xf1,
		0x00,
	)

	statedb, evm, caller, contractAddr := newTestEVMWithCode(t, code, nil)
	statedb.CreateAccount(child)
	statedb.SetCode(child, []byte{0xfe})

	const gasLimit = uint64(100_000)
	startConsumed := evm.TopLevelGasConsumed()
	_, leftOverGas, err := evm.Call(AccountRef(caller), contractAddr, nil, gasLimit, uint256.NewInt(0))
	require.NoError(t, err)

	assertTopLevelRunDelta(t, evm, startConsumed, gasLimit, leftOverGas)
}

func TestAddTopLevelGasConsumedAdvancesCounter(t *testing.T) {
	_, evm, _, _ := newTestEVMWithCode(t, nil, nil)

	before := evm.TopLevelGasConsumed()
	evm.ConsumeTopLevelGas(500)
	require.Equal(t, before+500, evm.TopLevelGasConsumed(), "ConsumeTopLevelGas should advance the counter by delta")

	evm.ConsumeTopLevelGas(300)
	require.Equal(t, before+800, evm.TopLevelGasConsumed(), "second call should accumulate with first")
}

func TestAddTopLevelGasConsumedZeroDeltaIsNoOp(t *testing.T) {
	_, evm, _, _ := newTestEVMWithCode(t, nil, nil)

	before := evm.TopLevelGasConsumed()
	evm.ConsumeTopLevelGas(0)
	require.Equal(t, before, evm.TopLevelGasConsumed(), "zero-delta ConsumeTopLevelGas should not change the counter")
}

func TestAddTopLevelGasConsumedAccumulatesWithExecutionGas(t *testing.T) {
	// Verify that AddTopLevelGasConsumed advances the counter independently of
	// a transaction execution, and that subsequent executions still accumulate
	// on top of the manually-added delta.
	code := []byte{0x60, 0x00, 0x50, 0x00} // PUSH 0, POP, STOP
	_, evm, caller, contractAddr := newTestEVMWithCode(t, code, nil)

	evm.ConsumeTopLevelGas(1000)
	afterManual := evm.TopLevelGasConsumed()
	require.Equal(t, uint64(1000), afterManual)

	const gasLimit = uint64(50_000)
	_, leftOverGas, err := evm.Call(AccountRef(caller), contractAddr, nil, gasLimit, uint256.NewInt(0))
	require.NoError(t, err)
	runConsumed := gasLimit - leftOverGas
	require.Equal(t, afterManual+runConsumed, evm.TopLevelGasConsumed(),
		"execution gas should accumulate on top of manually-added delta")
}
