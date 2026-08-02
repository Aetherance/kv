package util

import badger "github.com/dgraph-io/badger/v4"

type Iterator interface {
	Item() Item
	Valid() bool
	Next()
	Seek([]byte)
	Close()
}

type Item interface {
	Key() []byte
	KeyCopy(dst []byte) []byte
	Value() ([]byte, error)
	ValueSize() int
	ValueCopy(dst []byte) ([]byte, error)
}

type CFIterator struct {
	iter   *badger.Iterator
	prefix string
}

func NewCFIterator(cf string, txn *badger.Txn) *CFIterator {
	return &CFIterator{
		iter:   txn.NewIterator(badger.DefaultIteratorOptions),
		prefix: cf + "_",
	}
}

func (it *CFIterator) Item() Item {
	return &CFItem{
		item:      it.iter.Item(),
		prefixLen: len(it.prefix),
	}
}

func (it *CFIterator) Valid() bool {
	return it.iter.ValidForPrefix([]byte(it.prefix))
}

func (it *CFIterator) Close() {
	it.iter.Close()
}

func (it *CFIterator) Next() {
	it.iter.Next()
}

func (it *CFIterator) Seek(key []byte) {
	it.iter.Seek(append([]byte(it.prefix), key...))
}

type CFItem struct {
	item      *badger.Item
	prefixLen int
}

func (i *CFItem) Key() []byte {
	return i.item.Key()[i.prefixLen:]
}

func (i *CFItem) KeyCopy(dst []byte) []byte {
	key := i.item.KeyCopy(dst)
	return key[i.prefixLen:]
}

func (i *CFItem) Value() ([]byte, error) {
	return i.item.ValueCopy(nil)
}

func (i *CFItem) ValueSize() int {
	return int(i.item.ValueSize())
}

func (i *CFItem) ValueCopy(dst []byte) ([]byte, error) {
	return i.item.ValueCopy(dst)
}
