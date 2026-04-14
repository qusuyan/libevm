package state

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ava-labs/libevm/common"
	"github.com/ava-labs/libevm/core/rawdb"
	"github.com/ava-labs/libevm/core/state/snapshot"
	"github.com/ava-labs/libevm/core/types"
	"github.com/ava-labs/libevm/crypto"
	"github.com/ava-labs/libevm/ethdb"
	"github.com/ava-labs/libevm/metrics"
	"github.com/ava-labs/libevm/trie"
	"github.com/ava-labs/libevm/trie/trienode"
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
	if value == nil || *value != slotVal {
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
	if value == nil || *value != slotVal {
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
	if value != nil {
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
			if value == nil || *value != slotVal {
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

type overlapCounter struct {
	current atomic.Int32
	max     atomic.Int32
}

func (c *overlapCounter) enter() func() {
	current := c.current.Add(1)
	for {
		prev := c.max.Load()
		if current <= prev || c.max.CompareAndSwap(prev, current) {
			break
		}
	}
	return func() {
		c.current.Add(-1)
	}
}

type slowTrie struct {
	account      *types.StateAccount
	storage      common.Hash
	accountDelay time.Duration
	storageDelay time.Duration
	accountReads *overlapCounter
	storageReads *overlapCounter
}

func (t *slowTrie) GetKey([]byte) []byte { return nil }

func (t *slowTrie) GetAccount(common.Address) (*types.StateAccount, error) {
	done := t.accountReads.enter()
	defer done()
	time.Sleep(t.accountDelay)
	if t.account == nil {
		return nil, nil
	}
	return t.account.Copy(), nil
}

func (t *slowTrie) GetStorage(common.Address, []byte) ([]byte, error) {
	done := t.storageReads.enter()
	defer done()
	time.Sleep(t.storageDelay)
	return t.storage.Bytes(), nil
}

func (t *slowTrie) UpdateAccount(common.Address, *types.StateAccount) error { panic("unused") }
func (t *slowTrie) UpdateStorage(common.Address, []byte, []byte) error      { panic("unused") }
func (t *slowTrie) DeleteAccount(common.Address) error                      { panic("unused") }
func (t *slowTrie) DeleteStorage(common.Address, []byte) error              { panic("unused") }
func (t *slowTrie) UpdateContractCode(common.Address, common.Hash, []byte) error {
	panic("unused")
}
func (t *slowTrie) Hash() common.Hash { return common.Hash{} }
func (t *slowTrie) Commit(bool) (common.Hash, *trienode.NodeSet, error) {
	panic("unused")
}
func (t *slowTrie) NodeIterator([]byte) (trie.NodeIterator, error) { panic("unused") }
func (t *slowTrie) Prove([]byte, ethdb.KeyValueWriter) error       { panic("unused") }

type slowDatabase struct {
	accountTrie      *slowTrie
	storageTrie      *slowTrie
	code             []byte
	copyDelay        time.Duration
	codeDelay        time.Duration
	openStorageDelay time.Duration
	codeReads        *overlapCounter
	openStorageReads *overlapCounter
}

func (db *slowDatabase) OpenTrie(common.Hash) (Trie, error) { return db.accountTrie, nil }

func (db *slowDatabase) OpenStorageTrie(common.Hash, common.Address, common.Hash, Trie) (Trie, error) {
	done := db.openStorageReads.enter()
	defer done()
	time.Sleep(db.openStorageDelay)
	return db.storageTrie, nil
}

func (db *slowDatabase) CopyTrie(Trie) Trie {
	time.Sleep(db.copyDelay)
	return db.accountTrie
}

func (db *slowDatabase) ContractCode(common.Address, common.Hash) ([]byte, error) {
	done := db.codeReads.enter()
	defer done()
	time.Sleep(db.codeDelay)
	return append([]byte(nil), db.code...), nil
}

func (db *slowDatabase) ContractCodeSize(common.Address, common.Hash) (int, error) {
	return len(db.code), nil
}

func (db *slowDatabase) DiskDB() ethdb.KeyValueStore { return rawdb.NewMemoryDatabase() }
func (db *slowDatabase) TrieDB() *triedb.Database    { return nil }

func TestParallelReaderCopyAllowsParallelAccountReads(t *testing.T) {
	t.Parallel()

	accountReads := &overlapCounter{}
	codeReads := &overlapCounter{}
	account := &types.StateAccount{
		Nonce:    11,
		Balance:  uint256.NewInt(7),
		Root:     types.EmptyRootHash,
		CodeHash: common.HexToHash("0x1234").Bytes(),
		Extra:    &types.StateAccountExtra{},
	}
	db := &slowDatabase{
		accountTrie: &slowTrie{
			account:      account,
			accountDelay: 30 * time.Millisecond,
			accountReads: accountReads,
			storageReads: &overlapCounter{},
		},
		storageTrie:      &slowTrie{storageReads: &overlapCounter{}},
		code:             []byte{0x60, 0x01},
		codeDelay:        30 * time.Millisecond,
		codeReads:        codeReads,
		openStorageReads: &overlapCounter{},
	}
	reader := &ParallelReader{
		db:           db,
		trie:         db.accountTrie,
		hasher:       crypto.NewKeccakState(),
		storageTries: make(map[common.Address]Trie),
	}

	const readers = 8
	addr := common.HexToAddress("0x1234")
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, readers)

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := reader.Copy()
			<-start
			account, err := local.GetAccount(addr)
			if err != nil {
				errCh <- err
				return
			}
			if account == nil || account.Data == nil {
				errCh <- testingError("expected account")
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	if got := accountReads.max.Load(); got < 2 {
		t.Fatalf("expected overlapping trie account reads, max concurrency = %d", got)
	}
	if got := codeReads.max.Load(); got < 2 {
		t.Fatalf("expected overlapping code reads, max concurrency = %d", got)
	}
}

func TestParallelReaderCopyAllowsParallelStorageReads(t *testing.T) {
	t.Parallel()

	openStorageReads := &overlapCounter{}
	storageReads := &overlapCounter{}
	db := &slowDatabase{
		accountTrie: &slowTrie{
			account:      &types.StateAccount{},
			accountReads: &overlapCounter{},
			storageReads: &overlapCounter{},
		},
		storageTrie: &slowTrie{
			storage:      common.HexToHash("0xbeef"),
			storageDelay: 30 * time.Millisecond,
			accountReads: &overlapCounter{},
			storageReads: storageReads,
		},
		openStorageDelay: 30 * time.Millisecond,
		codeReads:        &overlapCounter{},
		openStorageReads: openStorageReads,
	}
	reader := &ParallelReader{
		db:           db,
		trie:         db.accountTrie,
		hasher:       crypto.NewKeccakState(),
		storageTries: make(map[common.Address]Trie),
	}

	const readers = 8
	addr := common.HexToAddress("0x1234")
	slot := common.HexToHash("0x1")
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, readers)

	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := reader.Copy()
			<-start
			value, err := local.GetState(addr, common.HexToHash("0x4567"), slot)
			if err != nil {
				errCh <- err
				return
			}
			if value == nil || *value != db.storageTrie.storage {
				errCh <- testingErrorf("unexpected slot value: %v", value)
			}
		}()
	}

	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	if got := openStorageReads.max.Load(); got < 2 {
		t.Fatalf("expected overlapping storage trie opens, max concurrency = %d", got)
	}
	if got := storageReads.max.Load(); got < 2 {
		t.Fatalf("expected overlapping storage trie reads, max concurrency = %d", got)
	}
}

func TestParallelReaderStorageReadsAccumulateOnReaderUntilAccumulated(t *testing.T) {
	previous := metrics.EnabledExpensive
	metrics.EnabledExpensive = true
	defer func() {
		metrics.EnabledExpensive = previous
	}()

	db := &slowDatabase{
		accountTrie: &slowTrie{
			account:      &types.StateAccount{},
			accountReads: &overlapCounter{},
			storageReads: &overlapCounter{},
		},
		storageTrie: &slowTrie{
			storage:      common.HexToHash("0xbeef"),
			storageDelay: 20 * time.Millisecond,
			accountReads: &overlapCounter{},
			storageReads: &overlapCounter{},
		},
		openStorageDelay: 20 * time.Millisecond,
		codeReads:        &overlapCounter{},
		openStorageReads: &overlapCounter{},
	}
	base := &StateDB{
		db:     db,
		trie:   db.accountTrie,
		hasher: crypto.NewKeccakState(),
	}

	reader := NewParallelReader(base)
	if reader == nil {
		t.Fatal("expected parallel reader")
	}

	value, err := reader.GetState(common.HexToAddress("0x1234"), common.HexToHash("0x4567"), common.HexToHash("0x1"))
	if err != nil {
		t.Fatalf("GetState failed: %v", err)
	}
	if value == nil || *value != db.storageTrie.storage {
		t.Fatalf("unexpected slot value: %v", value)
	}
	if got := base.StorageReads; got != 0 {
		t.Fatalf("expected base StorageReads to remain unchanged before accumulate, got %s", got)
	}
	reader.AccumulateDurations(base)
	if got := base.StorageReads; got <= 0 {
		t.Fatalf("expected base StorageReads to increase after accumulate, got %s", got)
	}
}
