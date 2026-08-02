// Package server contains the KV server implementation.
package server

import (
	"context"

	"github.com/Aetherance/kv/engine/storage"
	"github.com/Aetherance/kv/proto/pkg/kvpb"
	"github.com/Aetherance/kv/proto/pkg/kvrpcpb"
)

type Server struct {
	kvpb.UnimplementedKvServer
	storage storage.Storage
}

func NewServer(s storage.Storage) *Server {
	return &Server{storage: s}
}

func (s *Server) KvGet(_ context.Context, req *kvrpcpb.KvGetRequest) (*kvrpcpb.KvGetResponse, error) {
	resp := &kvrpcpb.KvGetResponse{}

	reader, err := s.storage.Reader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	val, err := reader.GetCF(req.Cf, req.Key)
	if err != nil {
		return nil, err
	}
	if val == nil {
		resp.NotFound = true
		return resp, nil
	}
	resp.Value = val

	return resp, nil
}

func (s *Server) KvPut(_ context.Context, req *kvrpcpb.KvPutRequest) (*kvrpcpb.KvPutResponse, error) {
	resp := &kvrpcpb.KvPutResponse{}

	modify := storage.Modify{
		Data: storage.Put{
			Key: req.Key,
			Val: req.Value,
			Cf:  req.Cf,
		},
	}
	if err := s.storage.Write([]storage.Modify{modify}); err != nil {
		return nil, err
	}
	return resp, nil
}

func (s *Server) KvDelete(_ context.Context, req *kvrpcpb.KvDeleteRequest) (*kvrpcpb.KvDeleteResponse, error) {
	resp := &kvrpcpb.KvDeleteResponse{}

	modify := storage.Modify{
		Data: storage.Delete{
			Key: req.Key,
			Cf:  req.Cf,
		},
	}
	if err := s.storage.Write([]storage.Modify{modify}); err != nil {
		return nil, err
	}
	return resp, nil
}

func (s *Server) KvScan(_ context.Context, req *kvrpcpb.KvScanRequest) (*kvrpcpb.KvScanResponse, error) {
	resp := &kvrpcpb.KvScanResponse{}

	reader, err := s.storage.Reader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	iter := reader.IterCF(req.Cf)
	defer iter.Close()

	var count uint32
	for iter.Seek(req.StartKey); iter.Valid() && count < req.Limit; iter.Next() {
		item := iter.Item()
		key := item.KeyCopy(nil)
		val, err := item.ValueCopy(nil)
		if err != nil {
			return nil, err
		}
		resp.Kvs = append(resp.Kvs, &kvrpcpb.KvPair{
			Key:   key,
			Value: val,
		})
		count++
	}

	return resp, nil
}
