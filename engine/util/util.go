package util

import badger "github.com/dgraph-io/badger/v4"

func KeyWithCF(cf string, key []byte) []byte {
	return append([]byte(cf+"_"), key...)
}

func GetCFFromTxn(txn *badger.Txn, cf string, key []byte) (val []byte, err error) {
	item, err := txn.Get(KeyWithCF(cf, key))
	if err != nil {
		return nil, err
	}
	val, err = item.ValueCopy(val)
	return
}
