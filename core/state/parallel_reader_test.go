package state

import (
	"fmt"
	"sync"
	"testing"

	"github.com/ava-labs/libevm/common"
	"github.com/ava-labs/libevm/core/rawdb"
	"github.com/ava-labs/libevm/core/state/snapshot"
	"github.com/ava-labs/libevm/core/types"
	"github.com/ava-labs/libevm/triedb"
	"github.com/holiman/uint256"
)

func newParallelReaderTestState(t *testing.T, withSnapshots bool) (*StateDB, common.Address, common.Hash, []byte, common.Hash) {
	t.Helper()

	var (
		disk     = rawdb.NewMemoryDatabase()
		tdb      = triedb.NewDatabase(disk, nil)
		db       = NewDatabaseWithNodeDB(disk, tdb)
		snaps    SnapshotTree
		err      error
		addr     = common.HexToAddress("0x1234")
		slot     = common.HexToHash("0x1")
		slotVal  = common.HexToHash("0x2222")
		code     = []byte{0x60, 0x01, 0x60, 0x02}
		rootHash common.Hash
	)
	if withSnapshots {
		snaps, err = snapshot.New(snapshot.Config{CacheSize: 10}, disk, tdb, types.EmptyRootHash)
		if err != nil {
			t.Fatalf("snapshot.New failed: %v", err)
		}
	}
	statedb, err := New(types.EmptyRootHash, db, snaps)
	if err != nil {
		t.Fatalf("state.New failed: %v", err)
	}
	statedb.SetBalance(addr, uint256.NewInt(7))
	statedb.SetNonce(addr, 11)
	statedb.SetCode(addr, code)
	statedb.SetState(addr, slot, slotVal)
	statedb.SetExtra(addr, &types.StateAccountExtra{})

	rootHash, err = statedb.Commit(0, true)
	if err != nil {
		t.Fatalf("state.Commit failed: %v", err)
	}
	if err := tdb.Commit(rootHash, false); err != nil {
		t.Fatalf("triedb.Commit failed: %v", err)
	}

	reopened, err := New(rootHash, db, snaps)
	if err != nil {
		t.Fatalf("state.New(reopened) failed: %v", err)
	}
	return reopened, addr, slot, code, slotVal
}

func TestParallelReaderGetAccountAndStateTrieBacked(t *testing.T) {
	statedb, addr, slot, code, slotVal := newParallelReaderTestState(t, false)

	reader := NewParallelReader(statedb)
	account, err := reader.GetAccount(addr)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if account == nil || account.Data == nil {
		t.Fatal("expected account cache with state data")
	}
	if got := account.Data.Nonce; got != 11 {
		t.Fatalf("unexpected nonce: %d", got)
	}
	if got := account.Data.Balance; got.Cmp(uint256.NewInt(7)) != 0 {
		t.Fatalf("unexpected balance: %s", got)
	}
	if string(account.Code) != string(code) {
		t.Fatalf("unexpected code: %x", account.Code)
	}
	value, err := reader.GetState(addr, account.Data.Root, slot)
	if err != nil {
		t.Fatalf("GetState failed: %v", err)
	}
	if value != slotVal {
		t.Fatalf("unexpected slot value: %x", value)
	}
}

func TestParallelReaderGetAccountAndStateSnapshotBacked(t *testing.T) {
	statedb, addr, slot, code, slotVal := newParallelReaderTestState(t, true)

	reader := NewParallelReader(statedb)
	account, err := reader.GetAccount(addr)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	if account == nil || account.Data == nil {
		t.Fatal("expected account cache with state data")
	}
	if got := account.Data.Nonce; got != 11 {
		t.Fatalf("unexpected nonce: %d", got)
	}
	if got := account.Data.Balance; got.Cmp(uint256.NewInt(7)) != 0 {
		t.Fatalf("unexpected balance: %s", got)
	}
	if string(account.Code) != string(code) {
		t.Fatalf("unexpected code: %x", account.Code)
	}
	value, err := reader.GetState(addr, account.Data.Root, slot)
	if err != nil {
		t.Fatalf("GetState failed: %v", err)
	}
	if value != slotVal {
		t.Fatalf("unexpected slot value: %x", value)
	}
}

func TestParallelReaderGetStateEmptyRoot(t *testing.T) {
	statedb, _, slot, _, _ := newParallelReaderTestState(t, false)

	reader := NewParallelReader(statedb)
	value, err := reader.GetState(common.HexToAddress("0xbeef"), types.EmptyRootHash, slot)
	if err != nil {
		t.Fatalf("GetState failed: %v", err)
	}
	if value != (common.Hash{}) {
		t.Fatalf("unexpected slot value: %x", value)
	}
}

func TestStateDBPreloadParallelAccount(t *testing.T) {
	statedb, addr, slot, code, slotVal := newParallelReaderTestState(t, false)
	reader := NewParallelReader(statedb)

	cache, err := reader.GetAccount(addr)
	if err != nil {
		t.Fatalf("GetAccount failed: %v", err)
	}
	cache.Storage = map[common.Hash]common.Hash{
		slot: slotVal,
	}

	fresh, err := New(statedb.originalRoot, statedb.db, statedb.snaps)
	if err != nil {
		t.Fatalf("state.New failed: %v", err)
	}
	if err := fresh.PreloadParallelAccount(cache); err != nil {
		t.Fatalf("PreloadParallelAccount failed: %v", err)
	}

	obj := fresh.stateObjects[addr]
	if obj == nil {
		t.Fatal("expected preloaded state object")
	}
	if got := obj.Nonce(); got != 11 {
		t.Fatalf("unexpected nonce: %d", got)
	}
	if got := obj.Balance(); got.Cmp(uint256.NewInt(7)) != 0 {
		t.Fatalf("unexpected balance: %s", got)
	}
	if string(obj.Code()) != string(code) {
		t.Fatalf("unexpected code: %x", obj.Code())
	}
	if got := obj.originStorage[slot]; got != slotVal {
		t.Fatalf("unexpected cached slot: %x", got)
	}
}

func TestParallelReaderConcurrentSnapshotReads(t *testing.T) {
	statedb, addr, slot, code, slotVal := newParallelReaderTestState(t, true)
	reader := NewParallelReader(statedb)

	const readers = 16
	var wg sync.WaitGroup
	errCh := make(chan error, readers*2)

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			local := reader.Copy()
			account, err := local.GetAccount(addr)
			if err != nil {
				errCh <- err
				return
			}
			if account == nil || account.Data == nil {
				errCh <- testingError("expected account cache with state data")
				return
			}
			if got := account.Data.Nonce; got != 11 {
				errCh <- testingErrorf("unexpected nonce: %d", got)
				return
			}
			if got := account.Data.Balance; got.Cmp(uint256.NewInt(7)) != 0 {
				errCh <- testingErrorf("unexpected balance: %s", got)
				return
			}
			if string(account.Code) != string(code) {
				errCh <- testingErrorf("unexpected code: %x", account.Code)
				return
			}
			value, err := local.GetState(addr, account.Data.Root, slot)
			if err != nil {
				errCh <- err
				return
			}
			if value != slotVal {
				errCh <- testingErrorf("unexpected slot value: %x", value)
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
}

type testingError string

func (e testingError) Error() string { return string(e) }

func testingErrorf(format string, args ...any) error {
	return testingError(fmt.Sprintf(format, args...))
}
