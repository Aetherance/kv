package raft_storage

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Aetherance/kv/engine/config"
	"github.com/Aetherance/kv/proto/pkg/clusterpb"
	rspb "github.com/Aetherance/kv/proto/pkg/raft_serverpb"
)

func TestLearnerJoinsBySnapshotAndPromotes(t *testing.T) {
	leaderListener := listenLocal(t)
	learnerListener := listenLocal(t)
	leaderAddress := leaderListener.Addr().String()
	learnerAddress := learnerListener.Addr().String()

	leaderConfig := joinTestConfig(t, 1, map[uint64]string{1: leaderAddress}, false)
	// Keep the leader from dialing the reserved learner address before the
	// learner has finished initializing and its gRPC server starts serving.
	leaderConfig.RaftBaseTickInterval = 200 * time.Millisecond
	leader := NewRaftStorage(leaderConfig)
	if err := leader.Start(); err != nil {
		t.Fatalf("start leader: %v", err)
	}
	t.Cleanup(func() { _ = leader.Stop() })
	leaderServer := serveRaftAndCluster(t, leaderListener, leader)

	learnerConfig := joinTestConfig(t, 2, map[uint64]string{
		1: leaderAddress,
		2: learnerAddress,
	}, true)
	learner := NewRaftStorage(learnerConfig)

	addResponse, err := leader.MemberAdd(context.Background(), &clusterpb.MemberAddRequest{
		Id: 2, RaftAddress: learnerAddress, Learner: true,
	})
	if err != nil {
		t.Fatalf("add learner: %v", err)
	}
	if addResponse.Cluster.ConfRevision != 1 || leader.state.snapshot.Metadata.Index == 0 {
		t.Fatalf("learner add did not produce durable snapshot: %v", addResponse.Cluster)
	}

	joinInfo, err := leader.JoinInfo(context.Background(), &clusterpb.JoinInfoRequest{Id: 2, RaftAddress: learnerAddress})
	if err != nil {
		t.Fatalf("fetch join info: %v", err)
	}
	if joinInfo.ClusterId != 42 || len(joinInfo.Members) != 2 {
		t.Fatalf("unexpected join info: %v", joinInfo)
	}
	if _, err := leader.JoinInfo(context.Background(), &clusterpb.JoinInfoRequest{Id: 2, RaftAddress: "wrong"}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("wrong-address join code = %s, want FailedPrecondition", status.Code(err))
	}
	if _, err := leader.JoinInfo(context.Background(), &clusterpb.JoinInfoRequest{Id: 1, RaftAddress: leaderAddress}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("voter join code = %s, want FailedPrecondition", status.Code(err))
	}

	if err := learner.Start(); err != nil {
		t.Fatalf("start joining learner: %v", err)
	}
	t.Cleanup(func() { _ = learner.Stop() })
	learnerServer := serveRaftAndCluster(t, learnerListener, learner)
	waitForCondition(t, 8*time.Second, func() bool {
		list, err := learner.MemberList(context.Background(), &clusterpb.MemberListRequest{})
		return err == nil && list.ConfRevision == 1 && roleInList(list, 2) == clusterpb.MemberRole_MemberRoleLearner
	}, "learner to install membership snapshot")

	waitForCondition(t, 8*time.Second, func() bool {
		memberStatus, err := leader.MemberStatus(context.Background(), &clusterpb.MemberStatusRequest{Id: 2})
		return err == nil && memberStatus.Member.Active && memberStatus.Member.MatchIndex >= memberStatus.CommitIndex
	}, "learner to catch up")
	if _, err := leader.MemberPromote(context.Background(), &clusterpb.MemberPromoteRequest{Id: 2}); err != nil {
		t.Fatalf("promote learner: %v", err)
	}
	waitForCondition(t, 8*time.Second, func() bool {
		list, err := learner.MemberList(context.Background(), &clusterpb.MemberListRequest{})
		return err == nil && list.ConfRevision == 2 && roleInList(list, 2) == clusterpb.MemberRole_MemberRoleVoter
	}, "promoted voter state to reach joined member")

	learnerServer.Stop()
	leaderServer.Stop()
	if err := learner.Stop(); err != nil {
		t.Fatalf("stop learner: %v", err)
	}
	if err := leader.Stop(); err != nil {
		t.Fatalf("stop leader: %v", err)
	}
}

func joinTestConfig(t *testing.T, id uint64, peers map[uint64]string, join bool) *config.Config {
	t.Helper()
	cfg := config.NewDefaultConfig()
	cfg.StoreID = id
	cfg.ClusterID = 42
	cfg.Peers = peers
	cfg.Join = join
	cfg.DBPath = t.TempDir()
	cfg.RaftBaseTickInterval = 10 * time.Millisecond
	cfg.RaftHeartbeatTicks = 1
	cfg.RaftElectionTimeoutTicks = 5
	return cfg
}

func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func serveRaftAndCluster(t *testing.T, listener net.Listener, store *RaftStorage) *grpc.Server {
	t.Helper()
	server := grpc.NewServer()
	rspb.RegisterRaftServiceServer(server, store)
	clusterpb.RegisterClusterServer(server, store)
	go func() {
		if err := server.Serve(listener); err != nil {
			// Stop/Close is expected during cleanup.
			return
		}
	}()
	t.Cleanup(server.Stop)
	return server
}

func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func roleInList(list *clusterpb.MemberListResponse, id uint64) clusterpb.MemberRole {
	for _, member := range list.Members {
		if member.Member != nil && member.Member.Id == id {
			return member.Role
		}
	}
	return clusterpb.MemberRole_MemberRoleUnknown
}
