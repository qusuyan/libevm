package state

import (
	"bytes"
	"fmt"

	"github.com/ava-labs/libevm/common"
	"github.com/ava-labs/libevm/core/state/snapshot"
	"github.com/ava-labs/libevm/core/types"
	"github.com/ava-labs/libevm/crypto"
	"github.com/ava-labs/libevm/rlp"
)

// ParallelAccountCache is a canonicalizable subset of account-local cached state.
// It can be loaded by speculative readers and later rehydrated into a canonical
// StateDB without sharing live stateObjects across execution contexts.
type ParallelAccountCache struct {
	Address common.Address
	Data    *types.StateAccount
	Code    []byte

	// Storage holds committed storage values already observed for this account.
	Storage Storage
}

type ParallelReader struct {
	db           Database
	trie         Trie
	snaps        SnapshotTree
	snap         snapshot.Snapshot
	originalRoot common.Hash
	hasher       crypto.KeccakState
	storageTries map[common.Address]Trie
}

func NewParallelReader(base *StateDB) *ParallelReader {
	if base == nil {
		return nil
	}
	return &ParallelReader{
		db:           base.db,
		trie:         base.db.CopyTrie(base.trie),
		snaps:        base.snaps,
		snap:         base.snap,
		originalRoot: base.originalRoot,
		hasher:       crypto.NewKeccakState(),
		storageTries: make(map[common.Address]Trie),
	}
}

func (r *ParallelReader) Copy() *ParallelReader {
	if r == nil {
		return nil
	}
	return &ParallelReader{
		db:           r.db,
		trie:         r.db.CopyTrie(r.trie),
		snaps:        r.snaps,
		snap:         r.snap,
		originalRoot: r.originalRoot,
		hasher:       crypto.NewKeccakState(),
		storageTries: make(map[common.Address]Trie),
	}
}

func (r *ParallelReader) GetAccount(addr common.Address) (*ParallelAccountCache, error) {
	if r == nil {
		return nil, fmt.Errorf("nil parallel reader")
	}
	var (
		data   *types.StateAccount
		exists bool
		err    error
	)
	if r.snap != nil {
		data, exists, err = r.getAccountFromSnapshot(addr)
		if err == nil && exists {
			return r.newAccountCache(addr, data)
		}
		if err == nil && !exists {
			return nil, nil
		}
	}
	data, exists, err = r.getAccountFromTrie(addr)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	return r.newAccountCache(addr, data)
}

func (r *ParallelReader) GetState(addr common.Address, root common.Hash, slot common.Hash) (*common.Hash, error) {
	if r == nil {
		return nil, fmt.Errorf("nil parallel reader")
	}
	if root == types.EmptyRootHash {
		return nil, nil
	}
	if r.snap != nil {
		enc, err := r.snap.Storage(crypto.HashData(r.hasher, addr.Bytes()), crypto.Keccak256Hash(slot.Bytes()))
		if err == nil {
			if len(enc) == 0 {
				return nil, nil
			}
			_, content, _, err := rlp.Split(enc)
			if err != nil {
				return nil, err
			}
			value := common.BytesToHash(content)
			return &value, nil
		}
	}
	tr, err := r.getStorageTrie(addr, root)
	if err != nil {
		return nil, err
	}
	val, err := tr.GetStorage(addr, slot.Bytes())
	if err != nil {
		return nil, err
	}
	value := common.Hash{}
	value.SetBytes(val)
	return &value, nil
}

func (r *ParallelReader) newAccountCache(addr common.Address, data *types.StateAccount) (*ParallelAccountCache, error) {
	cache := &ParallelAccountCache{
		Address: addr,
		Data:    data,
	}
	if data == nil || bytes.Equal(data.CodeHash, types.EmptyCodeHash.Bytes()) {
		return cache, nil
	}
	code, err := r.db.ContractCode(addr, common.BytesToHash(data.CodeHash))
	if err != nil {
		return nil, err
	}
	cache.Code = code
	return cache, nil
}

func (r *ParallelReader) getAccountFromSnapshot(addr common.Address) (*types.StateAccount, bool, error) {
	acc, err := r.snap.Account(crypto.HashData(r.hasher, addr.Bytes()))
	if err != nil {
		return nil, false, err
	}
	if acc == nil {
		return nil, false, nil
	}
	data := &types.StateAccount{
		Nonce:    acc.Nonce,
		Balance:  acc.Balance,
		CodeHash: acc.CodeHash,
		Root:     common.BytesToHash(acc.Root),
		Extra:    acc.Extra,
	}
	if len(data.CodeHash) == 0 {
		data.CodeHash = types.EmptyCodeHash.Bytes()
	}
	if data.Root == (common.Hash{}) {
		data.Root = types.EmptyRootHash
	}
	return data, true, nil
}

func (r *ParallelReader) getAccountFromTrie(addr common.Address) (*types.StateAccount, bool, error) {
	data, err := r.trie.GetAccount(addr)
	if err != nil {
		return nil, false, err
	}
	if data == nil {
		return nil, false, nil
	}
	return data, true, nil
}

func (r *ParallelReader) getStorageTrie(addr common.Address, root common.Hash) (Trie, error) {
	if tr := r.storageTries[addr]; tr != nil {
		return tr, nil
	}
	tr, err := r.db.OpenStorageTrie(r.originalRoot, addr, root, r.trie)
	if err != nil {
		return nil, err
	}
	r.storageTries[addr] = tr
	return tr, nil
}

func (s *StateDB) PreloadParallelAccount(cache *ParallelAccountCache) error {
	if s == nil {
		return fmt.Errorf("nil statedb")
	}
	if cache == nil || cache.Data == nil {
		return nil
	}
	if _, exists := s.stateObjects[cache.Address]; exists {
		return nil
	}
	// This hook is only used during ordered WriteBack, after speculative
	// BlockState caches are no longer read. Under that contract we can reuse the
	// cached account payload directly instead of deep-copying it again here.
	obj := newObject(s, cache.Address, cache.Data)
	obj.code = Code(cache.Code)
	if cache.Storage != nil {
		obj.originStorage = cache.Storage
	}
	s.setStateObject(obj)
	return nil
}
