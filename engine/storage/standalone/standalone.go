package standalone

import (
	"fmt"
	"os"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/engine/storage"
	util "github.com/Aetherance/kv/engine/util"
	"github.com/Aetherance/kv/proto/pkg/kvrpcpb"
	badger "github.com/dgraph-io/badger/v4"
)

// StandAloneStorage is an implementation of Storage for a single-node KV instance.
// It does not communicate with other nodes and all data is stored locally.
type StandAloneStorage struct {
	db     *badger.DB
	dbPath string
}

func NewStandAloneStorage(conf *config.Config) *StandAloneStorage {
	return &StandAloneStorage{
		dbPath: conf.DBPath,
	}
}

func (s *StandAloneStorage) Start() error {
	if err := os.MkdirAll(s.dbPath, os.ModePerm); err != nil {
		return err
	}
	opts := badger.DefaultOptions(s.dbPath)
	db, err := badger.Open(opts)
	if err != nil {
		return err
	}
	s.db = db
	return nil
}

func (s *StandAloneStorage) Stop() error {
	if s.db == nil {
		return nil
	}
	db := s.db
	s.db = nil
	return db.Close()
}

func (s *StandAloneStorage) Reader(ctx *kvrpcpb.Context) (storage.StorageReader, error) {
	_ = ctx
	if s.db == nil {
		return nil, fmt.Errorf("db is nil")
	}
	return newStandAloneReader(s.db.NewTransaction(false)), nil
}

func (s *StandAloneStorage) Write(ctx *kvrpcpb.Context, batch []storage.Modify) error {
	_ = ctx
	if s.db == nil {
		return fmt.Errorf("db is nil")
	}
	do := func(txn *badger.Txn) error {
		for _, modify := range batch {
			switch data := modify.Data.(type) {
			case storage.Put:
				key := util.KeyWithCF(data.Cf, data.Key)
				if err := txn.Set(key, data.Val); err != nil {
					return err
				}
			case storage.Delete:
				key := util.KeyWithCF(data.Cf, data.Key)
				if err := txn.Delete(key); err != nil {
					return err
				}
			default:
				return fmt.Errorf("invalid batch type: %T", modify.Data)
			}
		}
		return nil
	}
	return s.db.Update(do)
}
