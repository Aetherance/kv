package raft_storage

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/Aetherance/kv/proto/pkg/clusterpb"
	"github.com/Aetherance/kv/proto/pkg/raftpb"
	"github.com/Aetherance/kv/raft"
)

func (rs *RaftStorage) MemberList(ctx context.Context, _ *clusterpb.MemberListRequest) (*clusterpb.MemberListResponse, error) {
	clusterStatus, err := rs.status(ctx)
	if err != nil {
		return nil, membershipRPCError(err)
	}
	return memberListResponse(clusterStatus), nil
}

func (rs *RaftStorage) MemberAdd(ctx context.Context, request *clusterpb.MemberAddRequest) (*clusterpb.MemberAddResponse, error) {
	if request == nil || !request.Learner {
		return nil, status.Error(codes.InvalidArgument, "members must first be added as learners")
	}
	member := &clusterpb.Member{Id: request.Id, RaftAddress: strings.TrimSpace(request.RaftAddress)}
	if err := rs.proposeMembership(ctx, raftpb.ConfChangeType_AddLearnerNode, member); err != nil {
		return nil, membershipRPCError(err)
	}
	list, err := rs.MemberList(ctx, &clusterpb.MemberListRequest{})
	return &clusterpb.MemberAddResponse{Cluster: list}, err
}

func (rs *RaftStorage) MemberPromote(ctx context.Context, request *clusterpb.MemberPromoteRequest) (*clusterpb.MemberPromoteResponse, error) {
	if request == nil || request.Id == 0 {
		return nil, status.Error(codes.InvalidArgument, "member ID must be non-zero")
	}
	clusterStatus, err := rs.status(ctx)
	if err != nil {
		return nil, membershipRPCError(err)
	}
	member, _ := findMember(clusterStatus.metadata, request.Id)
	if member == nil {
		return nil, membershipRPCError(errMemberNotFound)
	}
	if err := rs.proposeMembership(ctx, raftpb.ConfChangeType_AddNode, member); err != nil {
		return nil, membershipRPCError(err)
	}
	list, err := rs.MemberList(ctx, &clusterpb.MemberListRequest{})
	return &clusterpb.MemberPromoteResponse{Cluster: list}, err
}

func (rs *RaftStorage) MemberRemove(ctx context.Context, request *clusterpb.MemberRemoveRequest) (*clusterpb.MemberRemoveResponse, error) {
	if request == nil || request.Id == 0 {
		return nil, status.Error(codes.InvalidArgument, "member ID must be non-zero")
	}
	clusterStatus, err := rs.status(ctx)
	if err != nil {
		return nil, membershipRPCError(err)
	}
	member, _ := findMember(clusterStatus.metadata, request.Id)
	if member == nil {
		member = &clusterpb.Member{Id: request.Id}
	}
	if err := rs.proposeMembership(ctx, raftpb.ConfChangeType_RemoveNode, member); err != nil {
		return nil, membershipRPCError(err)
	}
	list, err := rs.MemberList(ctx, &clusterpb.MemberListRequest{})
	return &clusterpb.MemberRemoveResponse{Cluster: list}, err
}

func (rs *RaftStorage) MemberUpdate(ctx context.Context, request *clusterpb.MemberUpdateRequest) (*clusterpb.MemberUpdateResponse, error) {
	if request == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	member := &clusterpb.Member{Id: request.Id, RaftAddress: strings.TrimSpace(request.RaftAddress)}
	if err := rs.proposeMembership(ctx, raftpb.ConfChangeType_UpdateNode, member); err != nil {
		return nil, membershipRPCError(err)
	}
	list, err := rs.MemberList(ctx, &clusterpb.MemberListRequest{})
	return &clusterpb.MemberUpdateResponse{Cluster: list}, err
}

func (rs *RaftStorage) MemberStatus(ctx context.Context, request *clusterpb.MemberStatusRequest) (*clusterpb.MemberStatusResponse, error) {
	if request == nil || request.Id == 0 {
		return nil, status.Error(codes.InvalidArgument, "member ID must be non-zero")
	}
	clusterStatus, err := rs.status(ctx)
	if err != nil {
		return nil, membershipRPCError(err)
	}
	member, _ := findMember(clusterStatus.metadata, request.Id)
	if member == nil {
		return nil, membershipRPCError(errMemberNotFound)
	}
	return &clusterpb.MemberStatusResponse{
		LeaderId:    clusterStatus.leaderID,
		CommitIndex: clusterStatus.commitIndex,
		Member:      memberInfo(clusterStatus, member),
	}, nil
}

// JoinInfo returns the authoritative bootstrap view only for a member that has
// already been committed as a learner with the same advertised address.
func (rs *RaftStorage) JoinInfo(ctx context.Context, request *clusterpb.JoinInfoRequest) (*clusterpb.JoinInfoResponse, error) {
	if request == nil || request.Id == 0 || strings.TrimSpace(request.RaftAddress) == "" {
		return nil, status.Error(codes.InvalidArgument, "member ID and raft address are required")
	}
	clusterStatus, err := rs.status(ctx)
	if err != nil {
		return nil, membershipRPCError(err)
	}
	member, _ := findMember(clusterStatus.metadata, request.Id)
	if member == nil || member.RaftAddress != strings.TrimSpace(request.RaftAddress) {
		return nil, status.Error(codes.FailedPrecondition, "member must be added with this address before joining")
	}
	if memberRole(clusterStatus.confState, request.Id) != clusterpb.MemberRole_MemberRoleLearner {
		return nil, status.Error(codes.FailedPrecondition, "only an added learner may join with a fresh database")
	}
	members := make([]*clusterpb.Member, 0, len(clusterStatus.metadata.Members))
	for _, clusterMember := range clusterStatus.metadata.Members {
		members = append(members, proto.Clone(clusterMember).(*clusterpb.Member))
	}
	return &clusterpb.JoinInfoResponse{
		ClusterId: clusterStatus.metadata.ClusterId,
		LeaderId:  clusterStatus.leaderID,
		Members:   members,
	}, nil
}

func memberListResponse(clusterStatus *clusterStatus) *clusterpb.MemberListResponse {
	response := &clusterpb.MemberListResponse{
		ClusterId:    clusterStatus.metadata.ClusterId,
		LeaderId:     clusterStatus.leaderID,
		ConfRevision: clusterStatus.metadata.ConfRevision,
		Members:      make([]*clusterpb.MemberInfo, 0, len(clusterStatus.metadata.Members)),
	}
	for _, member := range clusterStatus.metadata.Members {
		response.Members = append(response.Members, memberInfo(clusterStatus, member))
	}
	return response
}

func memberInfo(clusterStatus *clusterStatus, member *clusterpb.Member) *clusterpb.MemberInfo {
	progress, exists := clusterStatus.progress[member.Id]
	active := exists && progress.RecentActive
	if member.Id == clusterStatus.leaderID {
		active = true
	}
	return &clusterpb.MemberInfo{
		Member:     proto.Clone(member).(*clusterpb.Member),
		Role:       memberRole(clusterStatus.confState, member.Id),
		Active:     active,
		MatchIndex: progress.Match,
	}
}

func membershipRPCError(err error) error {
	if err == nil {
		return nil
	}
	var notLeader *NotLeaderError
	switch {
	case errors.As(err, &notLeader):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, errMemberNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, errMemberRemoved), errors.Is(err, errMemberAlreadyExists), errors.Is(err, errAddressAlreadyExists):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, errLearnerNotReady), errors.Is(err, errLastVoter), errors.Is(err, errTooManyLearners), errors.Is(err, errUnsafeReconfiguration), errors.Is(err, raft.ErrConfChangePending):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, err.Error())
	default:
		return status.Error(codes.InvalidArgument, err.Error())
	}
}
