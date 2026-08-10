package raft_storage

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/Aetherance/kv/proto/pkg/clusterpb"
)

func initialClusterMetadata(clusterID uint64, peers map[uint64]string) (*clusterpb.ClusterMetadata, error) {
	metadata := &clusterpb.ClusterMetadata{ClusterId: clusterID}
	for id, address := range peers {
		if id == 0 || strings.TrimSpace(address) == "" {
			return nil, fmt.Errorf("raft storage: invalid initial member %d=%q", id, address)
		}
		metadata.Members = append(metadata.Members, &clusterpb.Member{
			Id:          id,
			RaftAddress: strings.TrimSpace(address),
		})
	}
	normalizeClusterMetadata(metadata)
	if err := validateClusterMetadata(metadata); err != nil {
		return nil, err
	}
	return metadata, nil
}

func cloneClusterMetadata(metadata *clusterpb.ClusterMetadata) *clusterpb.ClusterMetadata {
	if metadata == nil {
		return &clusterpb.ClusterMetadata{}
	}
	return proto.Clone(metadata).(*clusterpb.ClusterMetadata)
}

func normalizeClusterMetadata(metadata *clusterpb.ClusterMetadata) {
	if metadata == nil {
		return
	}
	sort.Slice(metadata.Members, func(i, j int) bool {
		return metadata.Members[i].Id < metadata.Members[j].Id
	})
	sort.Slice(metadata.RemovedMemberIds, func(i, j int) bool {
		return metadata.RemovedMemberIds[i] < metadata.RemovedMemberIds[j]
	})
}

func validateClusterMetadata(metadata *clusterpb.ClusterMetadata) error {
	if metadata == nil || metadata.ClusterId == 0 {
		return errors.New("raft storage: invalid cluster metadata")
	}
	if len(metadata.Members) == 0 {
		return errors.New("raft storage: cluster metadata has no members")
	}

	ids := make(map[uint64]struct{}, len(metadata.Members))
	addresses := make(map[string]uint64, len(metadata.Members))
	for _, member := range metadata.Members {
		if member == nil || member.Id == 0 || strings.TrimSpace(member.RaftAddress) == "" {
			return errors.New("raft storage: cluster metadata contains an invalid member")
		}
		if _, exists := ids[member.Id]; exists {
			return fmt.Errorf("raft storage: duplicate member ID %d", member.Id)
		}
		address := strings.TrimSpace(member.RaftAddress)
		if owner, exists := addresses[address]; exists {
			return fmt.Errorf("raft storage: address %q is shared by members %d and %d", address, owner, member.Id)
		}
		ids[member.Id] = struct{}{}
		addresses[address] = member.Id
	}

	removed := make(map[uint64]struct{}, len(metadata.RemovedMemberIds))
	for _, id := range metadata.RemovedMemberIds {
		if id == 0 {
			return errors.New("raft storage: removed member ID must be non-zero")
		}
		if _, duplicate := removed[id]; duplicate {
			return fmt.Errorf("raft storage: duplicate removed member ID %d", id)
		}
		if _, active := ids[id]; active {
			return fmt.Errorf("raft storage: member ID %d is both active and removed", id)
		}
		removed[id] = struct{}{}
	}
	return nil
}
