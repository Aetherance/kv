package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/Aetherance/kv/proto/pkg/clusterpb"
)

func runMember(client clusterpb.ClusterClient, args []string) {
	if len(args) == 0 {
		memberUsage()
	}
	ctx := context.Background()
	switch args[0] {
	case "list":
		response, err := client.MemberList(ctx, &clusterpb.MemberListRequest{})
		fatalIf(err)
		printMembers(response)

	case "add":
		fs := flag.NewFlagSet("member add", flag.ExitOnError)
		learner := fs.Bool("learner", false, "add as a non-voting learner")
		_ = fs.Parse(args[1:])
		remaining := fs.Args()
		if len(remaining) != 2 {
			fmt.Fprintln(os.Stderr, "usage: member add --learner <id> <raft-address>")
			os.Exit(1)
		}
		response, err := client.MemberAdd(ctx, &clusterpb.MemberAddRequest{
			Id:          parseMemberID(remaining[0]),
			RaftAddress: remaining[1],
			Learner:     *learner,
		})
		fatalIf(err)
		printMembers(response.Cluster)

	case "promote":
		requireMemberArgs(args, 2, "usage: member promote <id>")
		response, err := client.MemberPromote(ctx, &clusterpb.MemberPromoteRequest{Id: parseMemberID(args[1])})
		fatalIf(err)
		printMembers(response.Cluster)

	case "remove":
		requireMemberArgs(args, 2, "usage: member remove <id>")
		response, err := client.MemberRemove(ctx, &clusterpb.MemberRemoveRequest{Id: parseMemberID(args[1])})
		fatalIf(err)
		printMembers(response.Cluster)

	case "update":
		requireMemberArgs(args, 3, "usage: member update <id> <raft-address>")
		response, err := client.MemberUpdate(ctx, &clusterpb.MemberUpdateRequest{Id: parseMemberID(args[1]), RaftAddress: args[2]})
		fatalIf(err)
		printMembers(response.Cluster)

	case "status":
		requireMemberArgs(args, 2, "usage: member status <id>")
		response, err := client.MemberStatus(ctx, &clusterpb.MemberStatusRequest{Id: parseMemberID(args[1])})
		fatalIf(err)
		member := response.Member
		fmt.Printf("leader=%d commit=%d id=%d role=%s active=%t match=%d address=%s\n",
			response.LeaderId, response.CommitIndex, member.Member.Id, roleName(member.Role), member.Active, member.MatchIndex, member.Member.RaftAddress)

	default:
		memberUsage()
	}
}

func printMembers(response *clusterpb.MemberListResponse) {
	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(writer, "CLUSTER\tLEADER\tREVISION\tID\tROLE\tACTIVE\tMATCH\tRAFT ADDRESS\n")
	for _, member := range response.Members {
		fmt.Fprintf(writer, "%d\t%d\t%d\t%d\t%s\t%t\t%d\t%s\n",
			response.ClusterId, response.LeaderId, response.ConfRevision, member.Member.Id,
			roleName(member.Role), member.Active, member.MatchIndex, member.Member.RaftAddress)
	}
	_ = writer.Flush()
}

func roleName(role clusterpb.MemberRole) string {
	switch role {
	case clusterpb.MemberRole_MemberRoleVoter:
		return "voter"
	case clusterpb.MemberRole_MemberRoleLearner:
		return "learner"
	default:
		return "unknown"
	}
}

func parseMemberID(value string) uint64 {
	id, err := strconv.ParseUint(value, 10, 64)
	fatalIf(err)
	if id == 0 {
		fatalIf(fmt.Errorf("member ID must be non-zero"))
	}
	return id
}

func requireMemberArgs(args []string, count int, usage string) {
	if len(args) != count {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(1)
	}
}

func fatalIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func memberUsage() {
	fmt.Fprintln(os.Stderr, "usage: member <list|add|promote|remove|update|status> [args...]")
	os.Exit(1)
}
