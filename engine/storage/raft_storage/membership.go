package raft_storage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/Aetherance/kv/proto/pkg/clusterpb"
	"github.com/Aetherance/kv/proto/pkg/raftpb"
)

var (
	errMemberNotFound        = errors.New("raft storage: member not found")
	errMemberRemoved         = errors.New("raft storage: member ID was permanently removed")
	errMemberAlreadyExists   = errors.New("raft storage: member already exists")
	errAddressAlreadyExists  = errors.New("raft storage: raft address already belongs to another member")
	errLearnerNotReady       = errors.New("raft storage: learner is not caught up")
	errLastVoter             = errors.New("raft storage: cannot remove the last voter")
	errTooManyLearners       = errors.New("raft storage: only one learner may be added at a time")
	errUnsafeReconfiguration = errors.New("raft storage: reconfiguration would make the cluster unavailable")
	errMemberChangeApplied   = errors.New("raft storage: requested membership state is already applied")
)

type raftMemberRole uint8

const (
	memberRoleUnknown raftMemberRole = iota
	memberRoleVoter
	memberRoleLearner
)

func clusterAddresses(metadata *clusterpb.ClusterMetadata) map[uint64]string {
	addresses := make(map[uint64]string)
	if metadata == nil {
		return addresses
	}
	for _, member := range metadata.Members {
		if member != nil {
			addresses[member.Id] = member.RaftAddress
		}
	}
	return addresses
}

func findMember(metadata *clusterpb.ClusterMetadata, id uint64) (*clusterpb.Member, int) {
	if metadata == nil {
		return nil, -1
	}
	for index, member := range metadata.Members {
		if member != nil && member.Id == id {
			return member, index
		}
	}
	return nil, -1
}

func isRemovedMember(metadata *clusterpb.ClusterMetadata, id uint64) bool {
	if metadata == nil {
		return false
	}
	for _, removedID := range metadata.RemovedMemberIds {
		if removedID == id {
			return true
		}
	}
	return false
}

// applyClusterChange is deterministic and runs only for a committed
// ConfChange. RawNode updates voting roles; this function updates the durable
// application-level ID/address registry at the same applied index.
func applyClusterChange(metadata *clusterpb.ClusterMetadata, change *raftpb.ConfChange, context *clusterpb.ConfChangeContext) (*clusterpb.ClusterMetadata, error) {
	if change == nil || context == nil || context.Member == nil || context.Member.Id != change.NodeId {
		return nil, errors.New("raft storage: invalid conf change context")
	}
	next := cloneClusterMetadata(metadata)
	member, index := findMember(next, change.NodeId)

	switch change.ChangeType {
	case raftpb.ConfChangeType_AddLearnerNode:
		if member != nil {
			if member.RaftAddress != context.Member.RaftAddress {
				return nil, errMemberAlreadyExists
			}
			break
		}
		if isRemovedMember(next, change.NodeId) {
			return nil, errMemberRemoved
		}
		next.Members = append(next.Members, proto.Clone(context.Member).(*clusterpb.Member))

	case raftpb.ConfChangeType_AddNode:
		if member == nil {
			return nil, errMemberNotFound
		}

	case raftpb.ConfChangeType_UpdateNode:
		if member == nil {
			return nil, errMemberNotFound
		}
		next.Members[index].RaftAddress = context.Member.RaftAddress

	case raftpb.ConfChangeType_RemoveNode:
		if member == nil {
			return nil, errMemberNotFound
		}
		next.Members = append(next.Members[:index], next.Members[index+1:]...)
		next.RemovedMemberIds = append(next.RemovedMemberIds, change.NodeId)

	default:
		return nil, fmt.Errorf("raft storage: unsupported conf change %s", change.ChangeType)
	}

	next.ConfRevision++
	normalizeClusterMetadata(next)
	if err := validateClusterMetadata(next); err != nil {
		return nil, err
	}
	return next, nil
}

func roleOf(state *raftpb.ConfState, id uint64) raftMemberRole {
	if state != nil {
		for _, voter := range state.Voters {
			if voter == id {
				return memberRoleVoter
			}
		}
		for _, learner := range state.Learners {
			if learner == id {
				return memberRoleLearner
			}
		}
	}
	return memberRoleUnknown
}

func (rs *RaftStorage) validateConfChange(change *raftpb.ConfChange) error {
	var context clusterpb.ConfChangeContext
	if change == nil || proto.Unmarshal(change.GetContext(), &context) != nil || context.Member == nil || context.Member.Id != change.NodeId {
		return errors.New("raft storage: invalid conf change context")
	}
	if context.ProposerId == 0 || context.Sequence == 0 || change.NodeId == 0 {
		return errors.New("raft storage: invalid conf change identity")
	}

	address := strings.TrimSpace(context.Member.RaftAddress)
	member, _ := findMember(rs.state.cluster, change.NodeId)
	role := roleOf(rs.node.ConfState(), change.NodeId)
	switch change.ChangeType {
	case raftpb.ConfChangeType_AddLearnerNode:
		if member != nil {
			if role == memberRoleLearner && member.RaftAddress == address {
				return errMemberChangeApplied
			}
			return errMemberAlreadyExists
		}
		if isRemovedMember(rs.state.cluster, change.NodeId) {
			return errMemberRemoved
		}
		if address == "" {
			return errors.New("raft storage: member address is required")
		}
		if len(rs.node.ConfState().Learners) != 0 {
			return errTooManyLearners
		}
		if err := rs.checkUniqueAddress(change.NodeId, address); err != nil {
			return err
		}

	case raftpb.ConfChangeType_AddNode:
		if member == nil {
			return errMemberNotFound
		}
		if role == memberRoleVoter {
			return errMemberChangeApplied
		}
		if role != memberRoleLearner {
			return errors.New("raft storage: only a learner can be promoted")
		}
		progress, exists := rs.node.GetProgress()[change.NodeId]
		if !exists || !progress.RecentActive || progress.PendingSnapshot != 0 || progress.Match < rs.node.CommitIndex() {
			return errLearnerNotReady
		}

	case raftpb.ConfChangeType_UpdateNode:
		if member == nil {
			return errMemberNotFound
		}
		if address == "" {
			return errors.New("raft storage: member address is required")
		}
		if member.RaftAddress == address {
			return errMemberChangeApplied
		}
		if err := rs.checkUniqueAddress(change.NodeId, address); err != nil {
			return err
		}

	case raftpb.ConfChangeType_RemoveNode:
		if member == nil {
			if isRemovedMember(rs.state.cluster, change.NodeId) {
				return errMemberChangeApplied
			}
			return errMemberNotFound
		}
		if role == memberRoleVoter {
			if len(rs.node.ConfState().Voters) == 1 {
				return errLastVoter
			}
			if !rs.remainingVotersActive(change.NodeId) {
				return errUnsafeReconfiguration
			}
		}

	default:
		return fmt.Errorf("raft storage: unsupported conf change %s", change.ChangeType)
	}
	return nil
}

func (rs *RaftStorage) checkUniqueAddress(id uint64, address string) error {
	for _, member := range rs.state.cluster.Members {
		if member != nil && member.Id != id && member.RaftAddress == address {
			return errAddressAlreadyExists
		}
	}
	return nil
}

func (rs *RaftStorage) remainingVotersActive(removeID uint64) bool {
	confState := rs.node.ConfState()
	remaining := len(confState.Voters) - 1
	quorum := remaining/2 + 1
	active := 0
	progress := rs.node.GetProgress()
	for _, voterID := range confState.Voters {
		if voterID == removeID {
			continue
		}
		if voterID == rs.config.StoreID || progress[voterID].RecentActive {
			active++
		}
	}
	return active >= quorum
}

// proposeMembership is the single internal entry point used by later API
// adapters. Completion means the ConfChange and ClusterMetadata update have
// both reached the durable applied index.
func (rs *RaftStorage) proposeMembership(ctx context.Context, changeType raftpb.ConfChangeType, member *clusterpb.Member) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if member == nil {
		return errors.New("raft storage: member is required")
	}
	member = proto.Clone(member).(*clusterpb.Member)
	member.RaftAddress = strings.TrimSpace(member.RaftAddress)
	sequence := rs.sequence.Add(1)
	changeContext := &clusterpb.ConfChangeContext{
		ProposerId: rs.config.StoreID,
		Sequence:   sequence,
		Member:     member,
	}
	encodedContext, err := proto.Marshal(changeContext)
	if err != nil {
		return err
	}
	change := &raftpb.ConfChange{ChangeType: changeType, NodeId: member.Id, Context: encodedContext}
	resultCh := make(chan error, 1)

	rs.pendingMu.Lock()
	rs.pending[sequence] = resultCh
	rs.pendingMu.Unlock()

	if err := rs.proposeConfChangeData(ctx, change); err != nil {
		rs.removePending(sequence)
		if errors.Is(err, errMemberChangeApplied) {
			return nil
		}
		return err
	}
	select {
	case err := <-resultCh:
		return err
	case <-ctx.Done():
		rs.removePending(sequence)
		return ctx.Err()
	case <-rs.done:
		rs.removePending(sequence)
		return rs.stoppedError()
	}
}
