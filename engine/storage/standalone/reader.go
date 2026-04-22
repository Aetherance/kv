package standalone

import (
	util "github.com/Aetherance/kv/engine/util"
	badger "github.com/dgraph-io/badger/v4"
)

type standAloneReader struct {
	txn *badger.Txn
}

func newStandAloneReader(txn *badger.Txn) *standAloneReader {
	return &standAloneReader{
		txn: txn,
	}
}

func (r *standAloneReader) GetCF(cf string, key []byte) ([]byte, error) {
	var value []byte
	do := func(txn *badger.Txn) error {
		var err error
		value, err = util.GetCFFromTxn(txn, cf, key)
		if err == badger.ErrKeyNotFound {
			value = nil
			return nil
		}
		return err
	}
	if err := do(r.txn); err != nil {
		return nil, err
	}
	return value, nil
}

func (r *standAloneReader) IterCF(cf string) util.Iterator {
	return util.NewCFIterator(cf, r.txn)
}

func (r *standAloneReader) Close() {
	r.txn.Discard()
}
