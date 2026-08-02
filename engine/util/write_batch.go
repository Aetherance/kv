package util

import (
	badger "github.com/dgraph-io/badger/v4"
	"github.com/golang/protobuf/proto"
)

type WriteBatch struct {
	entries []*badger.Entry
}

const (
	CfDefault string = "default"
	CfWrite   string = "write"
	CfLock    string = "lock"
)

var CFs [3]string = [3]string{CfDefault, CfWrite, CfLock}

func (wb *WriteBatch) Len() int {
	return len(wb.entries)
}

func (wb *WriteBatch) SetCF(cf string, key, val []byte) {
	wb.entries = append(wb.entries, &badger.Entry{
		Key:   KeyWithCF(cf, key),
		Value: val,
	})
}

func (wb *WriteBatch) DeleteMeta(key []byte) {
	wb.entries = append(wb.entries, &badger.Entry{
		Key: key,
	})
}

func (wb *WriteBatch) DeleteCF(cf string, key []byte) {
	wb.entries = append(wb.entries, &badger.Entry{
		Key: KeyWithCF(cf, key),
	})
}

func (wb *WriteBatch) SetMeta(key []byte, msg proto.Message) error {
	val, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	wb.entries = append(wb.entries, &badger.Entry{
		Key:   key,
		Value: val,
	})
	return nil
}

func (wb *WriteBatch) WriteToDB(db *badger.DB) error {
	if len(wb.entries) > 0 {
		err := db.Update(func(txn *badger.Txn) error {
			for _, entry := range wb.entries {
				var err1 error
				if len(entry.Value) == 0 {
					err1 = txn.Delete(entry.Key)
				} else {
					err1 = txn.SetEntry(entry)
				}
				if err1 != nil {
					return err1
				}
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}
