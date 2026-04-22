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
	"testing"

	"github.com/ava-labs/libevm/params"
	"github.com/stretchr/testify/require"
)

func TestLookupSchedulerAccountingCostSLOAD(t *testing.T) {
	// Pre-EIP-2929: SLOAD has a constant gas of 800 (EIP-150), no schedulerAccountingGas.
	istanbulRules := params.Rules{IsEIP150: true, IsEIP158: true, IsHomestead: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true}
	cost, err := LookupSchedulerAccountingCost(istanbulRules, SLOAD)
	require.NoError(t, err)
	// In Istanbul the constantGas for SLOAD is 800; schedulerAccountingGas is 0.
	require.Equal(t, uint64(800), cost, "Istanbul SLOAD constant gas")

	// Post-EIP-2929 (Berlin+): SLOAD constantGas=0, schedulerAccountingGas=WarmStorageReadCostEIP2929=100.
	berlinRules := params.Rules{IsEIP150: true, IsEIP158: true, IsHomestead: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true, IsBerlin: true}
	cost, err = LookupSchedulerAccountingCost(berlinRules, SLOAD)
	require.NoError(t, err)
	require.Equal(t, params.WarmStorageReadCostEIP2929, cost, "Berlin SLOAD scheduler accounting cost")
}

func TestLookupSchedulerAccountingCostSSTORE(t *testing.T) {
	// Post-EIP-2929: SSTORE has schedulerAccountingGas=SstoreSetGasEIP2200=20000.
	berlinRules := params.Rules{IsEIP150: true, IsEIP158: true, IsHomestead: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true, IsBerlin: true}
	cost, err := LookupSchedulerAccountingCost(berlinRules, SSTORE)
	require.NoError(t, err)
	require.Equal(t, params.SstoreSetGasEIP2200, cost, "Berlin SSTORE scheduler accounting cost")
}

func TestLookupSchedulerAccountingCostLOG(t *testing.T) {
	berlinRules := params.Rules{IsEIP150: true, IsEIP158: true, IsHomestead: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true, IsBerlin: true}

	expectedLogCosts := [5]uint64{
		params.LogGas,                             // LOG0: 375
		params.LogGas + params.LogTopicGas,        // LOG1: 750
		params.LogGas + 2*params.LogTopicGas,      // LOG2: 1125
		params.LogGas + 3*params.LogTopicGas,      // LOG3: 1500
		params.LogGas + 4*params.LogTopicGas,      // LOG4: 1875
	}
	for n := 0; n <= 4; n++ {
		op := OpCode(int(LOG0) + n)
		cost, err := LookupSchedulerAccountingCost(berlinRules, op)
		require.NoError(t, err)
		require.Equal(t, expectedLogCosts[n], cost, "LOG%d scheduler accounting cost", n)
	}
}

func TestLookupSchedulerAccountingCostCREATE(t *testing.T) {
	berlinRules := params.Rules{IsEIP150: true, IsEIP158: true, IsHomestead: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true, IsBerlin: true}
	cost, err := LookupSchedulerAccountingCost(berlinRules, CREATE)
	require.NoError(t, err)
	require.Equal(t, params.CreateGas, cost, "CREATE scheduler accounting cost")
}

func TestLookupSchedulerAccountingCostSELFDESTRUCT(t *testing.T) {
	berlinRules := params.Rules{IsEIP150: true, IsEIP158: true, IsHomestead: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true, IsBerlin: true}
	cost, err := LookupSchedulerAccountingCost(berlinRules, SELFDESTRUCT)
	require.NoError(t, err)
	// SELFDESTRUCT constantGas = params.SelfdestructGasEIP150 = 5000 (set in enable2929/tangerine)
	require.Equal(t, uint64(5000), cost, "Berlin SELFDESTRUCT scheduler accounting cost")
}

func TestLookupSchedulerAccountingCostZeroForNonStateOpcodes(t *testing.T) {
	berlinRules := params.Rules{IsEIP150: true, IsEIP158: true, IsHomestead: true, IsByzantium: true, IsConstantinople: true, IsIstanbul: true, IsBerlin: true}
	// STOP has constantGas=0 and no schedulerAccountingGas, so cost should be 0.
	cost, err := LookupSchedulerAccountingCost(berlinRules, STOP)
	require.NoError(t, err)
	require.Equal(t, uint64(0), cost, "STOP should have zero scheduler accounting cost")

	// ADD has constantGas=3 (GasFastestStep), no schedulerAccountingGas.
	cost, err = LookupSchedulerAccountingCost(berlinRules, ADD)
	require.NoError(t, err)
	require.Equal(t, uint64(3), cost, "ADD should have scheduler cost equal to its constantGas")
}

func TestSchedulerKeccak256MemoryCost(t *testing.T) {
	tests := []struct {
		byteLen  int
		expected uint64
	}{
		{0, 0},
		{-1, 0},
		{1, params.Keccak256WordGas},     // 1 byte = 1 word
		{32, params.Keccak256WordGas},    // exactly 1 word
		{33, 2 * params.Keccak256WordGas}, // 33 bytes = 2 words (ceil)
		{64, 2 * params.Keccak256WordGas},
		{65, 3 * params.Keccak256WordGas},
	}
	for _, tt := range tests {
		got := SchedulerKeccak256MemoryCost(tt.byteLen)
		require.Equal(t, tt.expected, got, "SchedulerKeccak256MemoryCost(%d)", tt.byteLen)
	}
}
