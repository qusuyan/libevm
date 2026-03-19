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
	Storage map[common.Hash]common.Hash
}

func (c *ParallelAccountCache) Clone() *ParallelAccountCache {
	if c == nil {
		return nil
	}

	var data *types.StateAccount
	if c.Data != nil {
		data = c.Data.Copy()
	}
	var storage map[common.Hash]common.Hash
	if len(c.Storage) > 0 {
		storage = make(map[common.Hash]common.Hash, len(c.Storage))
		for key, value := range c.Storage {
			storage[key] = value
		}
	}
	return &ParallelAccountCache{
		Address: c.Address,
		Data:    data,
		Code:    common.CopyBytes(c.Code),
		Storage: storage,
	}
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

func (r *ParallelReader) GetState(addr common.Address, root common.Hash, slot common.Hash) (common.Hash, error) {
	if r == nil {
		return common.Hash{}, fmt.Errorf("nil parallel reader")
	}
	if root == types.EmptyRootHash {
		return common.Hash{}, nil
	}
	if r.snap != nil {
		enc, err := r.snap.Storage(crypto.HashData(r.hasher, addr.Bytes()), crypto.Keccak256Hash(slot.Bytes()))
		if err == nil {
			if len(enc) == 0 {
				return common.Hash{}, nil
			}
			value := common.Hash{}
			_, content, _, err := rlp.Split(enc)
			if err != nil {
				return common.Hash{}, err
			}
			value.SetBytes(content)
			return value, nil
		}
	}
	tr, err := r.getStorageTrie(addr, root)
	if err != nil {
		return common.Hash{}, err
	}
	val, err := tr.GetStorage(addr, slot.Bytes())
	if err != nil {
		return common.Hash{}, err
	}
	value := common.Hash{}
	value.SetBytes(val)
	return value, nil
}

func (r *ParallelReader) newAccountCache(addr common.Address, data *types.StateAccount) (*ParallelAccountCache, error) {
	cache := &ParallelAccountCache{
		Address: addr,
		Data:    data.Copy(),
	}
	if data == nil || bytes.Equal(data.CodeHash, types.EmptyCodeHash.Bytes()) {
		return cache, nil
	}
	code, err := r.db.ContractCode(addr, common.BytesToHash(data.CodeHash))
	if err != nil {
		return nil, err
	}
	cache.Code = common.CopyBytes(code)
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
	obj := newObject(s, cache.Address, cache.Data.Copy())
	obj.data = *cache.Data.Copy()
	obj.origin = cache.Data.Copy()
	obj.code = Code(common.CopyBytes(cache.Code))
	if len(cache.Storage) > 0 {
		obj.originStorage = make(Storage, len(cache.Storage))
		for key, value := range cache.Storage {
			obj.originStorage[key] = value
		}
	}
	s.setStateObject(obj)
	return nil
}

func (s *StateDB) PreloadParallelAccountsSlice(caches []*ParallelAccountCache) error {
	for _, cache := range caches {
		if err := s.PreloadParallelAccount(cache); err != nil {
			return err
		}
	}
	return nil
}
