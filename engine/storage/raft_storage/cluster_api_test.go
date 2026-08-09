package raft_storage

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Aetherance/kv/proto/pkg/clusterpb"
)

func TestClusterMembershipAPI(t *testing.T) {
	store := startMembershipStore(t)
	ctx := context.Background()

	list, err := store.MemberList(ctx, &clusterpb.MemberListRequest{})
	if err != nil {
		t.Fatalf("list initial members: %v", err)
	}
	assertMemberList(t, list, 42, 1, 0, []memberExpectation{{
		id: 1, address: "127.0.0.1:1", role: clusterpb.MemberRole_MemberRoleVoter, active: true,
	}})

	if _, err := store.MemberAdd(ctx, &clusterpb.MemberAddRequest{
		Id: 2, RaftAddress: "127.0.0.1:2",
	}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("direct voter add code = %s, want InvalidArgument", status.Code(err))
	}
	added, err := store.MemberAdd(ctx, &clusterpb.MemberAddRequest{
		Id: 2, RaftAddress: " 127.0.0.1:2 ", Learner: true,
	})
	if err != nil {
		t.Fatalf("add learner: %v", err)
	}
	assertMemberList(t, added.Cluster, 42, 1, 1, []memberExpectation{
		{id: 1, address: "127.0.0.1:1", role: clusterpb.MemberRole_MemberRoleVoter, active: true},
		{id: 2, address: "127.0.0.1:2", role: clusterpb.MemberRole_MemberRoleLearner},
	})

	memberStatus, err := store.MemberStatus(ctx, &clusterpb.MemberStatusRequest{Id: 2})
	if err != nil {
		t.Fatalf("learner status: %v", err)
	}
	if memberStatus.LeaderId != 1 || memberStatus.Member.Member.Id != 2 || memberStatus.Member.Role != clusterpb.MemberRole_MemberRoleLearner {
		t.Fatalf("unexpected learner status: %v", memberStatus)
	}

	updated, err := store.MemberUpdate(ctx, &clusterpb.MemberUpdateRequest{Id: 2, RaftAddress: "127.0.0.1:22"})
	if err != nil {
		t.Fatalf("update learner: %v", err)
	}
	assertMemberList(t, updated.Cluster, 42, 1, 2, []memberExpectation{
		{id: 1, address: "127.0.0.1:1", role: clusterpb.MemberRole_MemberRoleVoter, active: true},
		{id: 2, address: "127.0.0.1:22", role: clusterpb.MemberRole_MemberRoleLearner},
	})

	removed, err := store.MemberRemove(ctx, &clusterpb.MemberRemoveRequest{Id: 2})
	if err != nil {
		t.Fatalf("remove learner: %v", err)
	}
	assertMemberList(t, removed.Cluster, 42, 1, 3, []memberExpectation{{
		id: 1, address: "127.0.0.1:1", role: clusterpb.MemberRole_MemberRoleVoter, active: true,
	}})
	removed, err = store.MemberRemove(ctx, &clusterpb.MemberRemoveRequest{Id: 2})
	if err != nil || removed.Cluster.ConfRevision != 3 {
		t.Fatalf("idempotent remove = %v, %v", removed, err)
	}
	if _, err := store.MemberAdd(ctx, &clusterpb.MemberAddRequest{
		Id: 2, RaftAddress: "127.0.0.1:22", Learner: true,
	}); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("re-add removed member code = %s, want AlreadyExists", status.Code(err))
	}
}

func TestClusterMembershipAPIErrors(t *testing.T) {
	store := startMembershipStore(t)
	ctx := context.Background()

	if _, err := store.MemberStatus(ctx, &clusterpb.MemberStatusRequest{Id: 99}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown member status code = %s, want NotFound", status.Code(err))
	}
	if _, err := store.MemberRemove(ctx, &clusterpb.MemberRemoveRequest{Id: 99}); status.Code(err) != codes.NotFound {
		t.Fatalf("unknown member remove code = %s, want NotFound", status.Code(err))
	}
	if _, err := store.MemberRemove(ctx, &clusterpb.MemberRemoveRequest{Id: 1}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("last voter remove code = %s, want FailedPrecondition", status.Code(err))
	}
}

type memberExpectation struct {
	id      uint64
	address string
	role    clusterpb.MemberRole
	active  bool
}

func assertMemberList(t *testing.T, response *clusterpb.MemberListResponse, clusterID, leaderID, revision uint64, members []memberExpectation) {
	t.Helper()
	if response == nil || response.ClusterId != clusterID || response.LeaderId != leaderID || response.ConfRevision != revision {
		t.Fatalf("unexpected cluster response: %v", response)
	}
	if len(response.Members) != len(members) {
		t.Fatalf("members = %v, want %v", response.Members, members)
	}
	for index, want := range members {
		got := response.Members[index]
		if got.Member.Id != want.id || got.Member.RaftAddress != want.address || got.Role != want.role || got.Active != want.active {
			t.Fatalf("member %d = %v, want %+v", index, got, want)
		}
	}
}
