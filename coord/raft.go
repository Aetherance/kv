package coord

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/json"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/encoding"
)

type State int

const (
	follower State = iota
	leader
	candidate
)

type ApplyMsg struct {
	CommandValid bool
	Command      interface{}
	CommandIndex int
}

// RaftCommand is the replicated command. For now it only carries KV operations.
type RaftCommand struct {
	Op    int
	Key   []byte
	Value []byte
}

type LogEntry struct {
	Command RaftCommand
	Term    int
}

// ---- gRPC JSON codec (avoid protobuf generation for now) ----

type jsonCodec struct{}

func (jsonCodec) Marshal(v interface{}) ([]byte, error)      { return json.Marshal(v) }
func (jsonCodec) Unmarshal(data []byte, v interface{}) error { return json.Unmarshal(data, v) }
func (jsonCodec) Name() string                               { return "json" }

var registerCodecOnce sync.Once

// ---- Raft core ----

type Raft struct {
	mu         sync.Mutex
	peerAddrs  []string
	listenAddr string
	me         int
	dead       int32

	applyCh chan ApplyMsg

	conns      map[int]*grpc.ClientConn
	grpcServer *grpc.Server
	rpcTimeout time.Duration

	// persistent (in-memory for now)
	persistData []byte
	persistPath string

	// raft state
	state       State
	currentTerm int
	voteFor     int
	logs        []LogEntry

	commitIndex int
	lastApplied int

	nextIndex  []int
	matchIndex []int

	lastHeartBeat time.Time
	recvVotes     int

	leaderReady bool

	nextReadID uint64
	reads      map[uint64]*pendingRead
}

type pendingRead struct {
	term  int
	index int
	acks  map[int]struct{}
	done  chan struct{}
}

type AppendEntriesArgs struct {
	Term         int
	LeaderId     int
	PrevLogIndex int
	PrevLogTerm  int
	Entries      []LogEntry
	LeaderCommit int
}

type AppendEntriesReply struct {
	Term    int
	Success bool
}

type RequestVoteArgs struct {
	Term         int
	CandidateId  int
	LastLogIndex int
	LastLogTerm  int
}

type RequestVoteReply struct {
	Term        int
	VoteGranted bool
}

func NewRaft(me int, peerAddrs []string, listenAddr string, persistPath string, applyCh chan ApplyMsg) (*Raft, error) {
	registerCodecOnce.Do(func() {
		encoding.RegisterCodec(jsonCodec{})
	})

	rf := &Raft{
		peerAddrs:     append([]string(nil), peerAddrs...),
		listenAddr:    listenAddr,
		me:            me,
		voteFor:       -1,
		state:         follower,
		applyCh:       applyCh,
		logs:          []LogEntry{{Term: 0}},
		lastHeartBeat: time.Now(),
		conns:         make(map[int]*grpc.ClientConn),
		rpcTimeout:    500 * time.Millisecond,
		commitIndex:   0,
		lastApplied:   0,
		persistData:   nil,
		persistPath:   strings.TrimSpace(persistPath),
		currentTerm:   0,
		recvVotes:     0,
		nextIndex:     nil,
		matchIndex:    nil,
		leaderReady:   false,
		reads:         make(map[uint64]*pendingRead),
	}

	rf.readPersistFromDisk()
	if len(rf.logs) == 0 {
		rf.logs = []LogEntry{{Term: 0}}
	}

	if err := rf.startRPCServer(); err != nil {
		return nil, err
	}

	go rf.ticker()
	go rf.applier()
	return rf, nil
}

func (rf *Raft) startRPCServer() error {
	lis, err := net.Listen("tcp", rf.listenAddr)
	if err != nil {
		return err
	}
	srv := grpc.NewServer()
	registerRaftService(srv, &raftRPCServer{rf: rf})
	rf.grpcServer = srv
	go func() {
		_ = srv.Serve(lis)
	}()
	return nil
}

func (rf *Raft) GetState() (int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return rf.currentTerm, rf.state == leader
}

func (rf *Raft) PersistBytes() int {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	return len(rf.persistData)
}

func (rf *Raft) persist() {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	_ = enc.Encode(rf.currentTerm)
	_ = enc.Encode(rf.voteFor)
	_ = enc.Encode(rf.logs)
	rf.persistData = buf.Bytes()
	rf.savePersistToDiskLocked()
}

func (rf *Raft) readPersist(data []byte) {
	if len(data) == 0 {
		return
	}
	var term int
	var vote int
	var logs []LogEntry
	dec := gob.NewDecoder(bytes.NewReader(data))
	if dec.Decode(&term) != nil || dec.Decode(&vote) != nil || dec.Decode(&logs) != nil {
		return
	}
	rf.currentTerm = term
	rf.voteFor = vote
	rf.logs = logs
}

func (rf *Raft) readPersistFromDisk() {
	if rf.persistPath == "" {
		return
	}
	data, err := os.ReadFile(rf.persistPath)
	if err != nil {
		return
	}
	rf.readPersist(data)
}

func (rf *Raft) savePersistToDiskLocked() {
	// must be called under rf.mu (or during init) to keep persistData consistent.
	if rf.persistPath == "" {
		return
	}

	dir := filepath.Dir(rf.persistPath)
	_ = os.MkdirAll(dir, 0o755)

	f, err := os.CreateTemp(dir, "raft-*.tmp")
	if err != nil {
		return
	}
	tmp := f.Name()
	_, werr := f.Write(rf.persistData)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil || serr != nil || cerr != nil {
		_ = os.Remove(tmp)
		return
	}
	_ = os.Rename(tmp, rf.persistPath)
}

func (rf *Raft) Snapshot(index int, snapshot []byte) {}

func (rf *Raft) Start(command RaftCommand) (int, int, bool) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.state != leader {
		return -1, rf.currentTerm, false
	}
	idx := len(rf.logs)
	rf.logs = append(rf.logs, LogEntry{Command: command, Term: rf.currentTerm})
	if rf.matchIndex != nil {
		rf.matchIndex[rf.me] = idx
	}
	// Single-node cluster: leader commits immediately.
	if len(rf.peerAddrs) <= 1 {
		rf.commitIndex = idx
		if rf.logs[idx].Term == rf.currentTerm {
			rf.leaderReady = true
		}
	}
	rf.persist()
	return idx, rf.currentTerm, true
}

// ReadIndex returns an index that is safe for a linearizable read on the leader.
// Caller should wait until its state machine has applied through this index.
func (rf *Raft) ReadIndex(ctx context.Context) (int, bool, error) {
	rf.mu.Lock()
	if rf.state != leader {
		rf.mu.Unlock()
		return -1, false, nil
	}
	term := rf.currentTerm
	rf.mu.Unlock()

	if err := rf.waitLeaderReady(ctx, term); err != nil {
		rf.mu.Lock()
		isLeader := rf.state == leader && rf.currentTerm == term
		rf.mu.Unlock()
		if !isLeader {
			return -1, false, nil
		}
		return -1, true, err
	}

	rf.mu.Lock()
	if rf.state != leader || rf.currentTerm != term {
		rf.mu.Unlock()
		return -1, false, nil
	}
	if len(rf.peerAddrs) <= 1 {
		idx := rf.commitIndex
		rf.mu.Unlock()
		return idx, true, nil
	}

	rf.nextReadID++
	readID := rf.nextReadID
	pr := &pendingRead{
		term:  term,
		index: rf.commitIndex,
		acks:  map[int]struct{}{rf.me: {}},
		done:  make(chan struct{}),
	}
	rf.reads[readID] = pr
	rf.mu.Unlock()

	for i := range rf.peerAddrs {
		if i == rf.me {
			continue
		}
		go rf.sendReadHeartbeatTo(i, term, readID, pr.index)
	}

	select {
	case <-ctx.Done():
		rf.mu.Lock()
		delete(rf.reads, readID)
		rf.mu.Unlock()
		return -1, true, ctx.Err()
	case <-pr.done:
		rf.mu.Lock()
		defer rf.mu.Unlock()
		if rf.state != leader || rf.currentTerm != term {
			return -1, false, nil
		}
		return pr.index, true, nil
	}
}

func (rf *Raft) waitLeaderReady(ctx context.Context, term int) error {
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		rf.mu.Lock()
		ready := rf.state == leader && rf.currentTerm == term && rf.leaderReady
		rf.mu.Unlock()
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (rf *Raft) sendReadHeartbeatTo(peer int, term int, readID uint64, commitIndex int) {
	args := &AppendEntriesArgs{
		Term:         term,
		LeaderId:     rf.me,
		PrevLogIndex: -1,
		PrevLogTerm:  0,
		Entries:      nil,
		LeaderCommit: commitIndex,
	}
	reply := &AppendEntriesReply{}
	if !rf.sendAppendEntries(peer, args, reply) {
		return
	}
	if reply.Term != term || !reply.Success {
		return
	}
	rf.noteReadAck(readID, peer, term)
}

func (rf *Raft) noteReadAck(readID uint64, peer int, term int) {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.state != leader || rf.currentTerm != term {
		return
	}
	pr := rf.reads[readID]
	if pr == nil || pr.term != term {
		return
	}
	if _, ok := pr.acks[peer]; ok {
		return
	}
	pr.acks[peer] = struct{}{}
	if len(pr.acks) > len(rf.peerAddrs)/2 {
		delete(rf.reads, readID)
		close(pr.done)
	}
}

func (rf *Raft) Kill() {
	atomic.StoreInt32(&rf.dead, 1)
	rf.mu.Lock()
	defer rf.mu.Unlock()
	for _, conn := range rf.conns {
		_ = conn.Close()
	}
	if rf.grpcServer != nil {
		rf.grpcServer.Stop()
	}
}

func (rf *Raft) killed() bool { return atomic.LoadInt32(&rf.dead) == 1 }

// ---- RPC handlers (Raft core logic) ----

func (rf *Raft) AppendEntries(args *AppendEntriesArgs, reply *AppendEntriesReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	reply.Term = rf.currentTerm
	if args.Term < rf.currentTerm {
		reply.Success = false
		return
	}
	if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.state = follower
		rf.leaderReady = false
		rf.voteFor = -1
		rf.persist()
	}
	rf.lastHeartBeat = time.Now()

	if args.PrevLogIndex >= len(rf.logs) {
		reply.Success = false
		return
	}
	if args.PrevLogIndex >= 0 && rf.logs[args.PrevLogIndex].Term != args.PrevLogTerm {
		reply.Success = false
		return
	}

	insertAt := args.PrevLogIndex + 1
	i := 0
	for i < len(args.Entries) {
		if insertAt+i < len(rf.logs) {
			if rf.logs[insertAt+i].Term != args.Entries[i].Term {
				rf.logs = rf.logs[:insertAt+i]
				break
			}
			i++
			continue
		}
		break
	}
	for ; i < len(args.Entries); i++ {
		rf.logs = append(rf.logs, args.Entries[i])
	}
	rf.persist()

	if args.LeaderCommit > rf.commitIndex {
		last := len(rf.logs) - 1
		if args.LeaderCommit < last {
			rf.commitIndex = args.LeaderCommit
		} else {
			rf.commitIndex = last
		}
	}
	reply.Success = true
}

func (rf *Raft) RequestVote(args *RequestVoteArgs, reply *RequestVoteReply) {
	rf.mu.Lock()
	defer rf.mu.Unlock()

	reply.Term = rf.currentTerm
	reply.VoteGranted = false

	if args.Term < rf.currentTerm {
		return
	}
	if args.Term > rf.currentTerm {
		rf.currentTerm = args.Term
		rf.state = follower
		rf.leaderReady = false
		rf.voteFor = -1
		rf.persist()
	}

	lastIdx := len(rf.logs) - 1
	lastTerm := 0
	if lastIdx >= 0 {
		lastTerm = rf.logs[lastIdx].Term
	}
	if (args.LastLogTerm > lastTerm) || (args.LastLogTerm == lastTerm && args.LastLogIndex >= lastIdx) {
		if rf.voteFor == -1 || rf.voteFor == args.CandidateId {
			rf.voteFor = args.CandidateId
			rf.lastHeartBeat = time.Now()
			rf.persist()
			reply.VoteGranted = true
		}
	}
}

// ---- background loops ----

func (rf *Raft) applier() {
	for !rf.killed() {
		rf.mu.Lock()
		if rf.lastApplied >= rf.commitIndex {
			rf.mu.Unlock()
			time.Sleep(10 * time.Millisecond)
			continue
		}
		start := rf.lastApplied + 1
		end := rf.commitIndex
		toApply := make([]ApplyMsg, 0, end-start+1)
		for i := start; i <= end && i < len(rf.logs); i++ {
			toApply = append(toApply, ApplyMsg{
				CommandValid: true,
				Command:      rf.logs[i].Command,
				CommandIndex: i,
			})
		}
		rf.lastApplied = end
		rf.mu.Unlock()
		for _, m := range toApply {
			rf.applyCh <- m
		}
	}
}

func (rf *Raft) ticker() {
	for !rf.killed() {
		rf.mu.Lock()
		shouldElect := rf.state != leader && rf.serverTimeout()
		rf.mu.Unlock()
		if shouldElect {
			go rf.startElection()
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (rf *Raft) serverTimeout() bool {
	return time.Since(rf.lastHeartBeat) > time.Duration(150+rand.Int63()%300)*time.Millisecond
}

func (rf *Raft) startElection() {
	rf.mu.Lock()
	rf.currentTerm++
	rf.voteFor = rf.me
	rf.recvVotes = 1
	rf.state = candidate
	rf.lastHeartBeat = time.Now()
	rf.persist()
	term := rf.currentTerm
	rf.mu.Unlock()

	for peerIndex := range rf.peerAddrs {
		if peerIndex == rf.me {
			continue
		}
		go rf.votation(peerIndex, term)
	}
}

func (rf *Raft) votation(peerIndex, term int) {
	rf.mu.Lock()
	if rf.state != candidate || rf.currentTerm != term {
		rf.mu.Unlock()
		return
	}
	args := RequestVoteArgs{
		Term:         rf.currentTerm,
		CandidateId:  rf.me,
		LastLogIndex: len(rf.logs) - 1,
	}
	if args.LastLogIndex >= 0 {
		args.LastLogTerm = rf.logs[args.LastLogIndex].Term
	}
	rf.mu.Unlock()

	reply := RequestVoteReply{}
	ok := rf.sendRequestVote(peerIndex, &args, &reply)

	rf.mu.Lock()
	if ok && reply.Term > rf.currentTerm {
		rf.state = follower
		rf.leaderReady = false
		rf.voteFor = -1
		rf.currentTerm = reply.Term
		rf.persist()
		rf.mu.Unlock()
		return
	}
	if ok && reply.VoteGranted && rf.state == candidate && rf.currentTerm == term {
		rf.recvVotes++
	}
	rf.mu.Unlock()

	rf.checkIsBecomeLeader()
}

func (rf *Raft) checkIsBecomeLeader() {
	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.recvVotes > len(rf.peerAddrs)/2 && rf.state == candidate {
		rf.state = leader
		rf.leaderReady = false
		n := len(rf.peerAddrs)
		rf.nextIndex = make([]int, n)
		rf.matchIndex = make([]int, n)
		lastIdx := len(rf.logs) - 1
		for i := 0; i < n; i++ {
			rf.nextIndex[i] = lastIdx + 1
			rf.matchIndex[i] = 0
		}
		rf.matchIndex[rf.me] = lastIdx
		go rf.leaderIssuesItsOrder()
		// commit a no-op in current term so ReadIndex becomes safe
		go func() { _, _, _ = rf.Start(RaftCommand{}) }()
	}
}

func (rf *Raft) leaderIssuesItsOrder() {
	for !rf.killed() {
		rf.mu.Lock()
		if rf.state != leader {
			rf.mu.Unlock()
			return
		}
		term := rf.currentTerm
		rf.mu.Unlock()

		for i := range rf.peerAddrs {
			if i == rf.me {
				continue
			}
			go rf.sendAppendEntriesTo(i, term)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (rf *Raft) sendAppendEntriesTo(peer int, term int) {
	rf.mu.Lock()
	if rf.state != leader || rf.currentTerm != term {
		rf.mu.Unlock()
		return
	}
	prevIdx := rf.nextIndex[peer] - 1
	prevTerm := 0
	if prevIdx >= 0 {
		prevTerm = rf.logs[prevIdx].Term
	}
	entries := make([]LogEntry, len(rf.logs)-rf.nextIndex[peer])
	copy(entries, rf.logs[rf.nextIndex[peer]:])
	args := &AppendEntriesArgs{
		Term:         term,
		LeaderId:     rf.me,
		PrevLogIndex: prevIdx,
		PrevLogTerm:  prevTerm,
		Entries:      entries,
		LeaderCommit: rf.commitIndex,
	}
	rf.mu.Unlock()

	reply := &AppendEntriesReply{}
	ok := rf.sendAppendEntries(peer, args, reply)

	rf.mu.Lock()
	defer rf.mu.Unlock()
	if rf.state != leader || rf.currentTerm != term || !ok {
		return
	}
	if reply.Term > rf.currentTerm {
		rf.currentTerm = reply.Term
		rf.state = follower
		rf.leaderReady = false
		rf.voteFor = -1
		rf.persist()
		return
	}
	if !reply.Success {
		if rf.nextIndex[peer] > 1 {
			rf.nextIndex[peer]--
		}
		return
	}

	match := prevIdx + len(entries)
	rf.matchIndex[peer] = match
	rf.nextIndex[peer] = match + 1

	for n := len(rf.logs) - 1; n > rf.commitIndex && rf.logs[n].Term == term; n-- {
		count := 1
		for i := range rf.peerAddrs {
			if i != rf.me && rf.matchIndex[i] >= n {
				count++
			}
		}
		if count > len(rf.peerAddrs)/2 {
			rf.commitIndex = n
			if rf.logs[n].Term == rf.currentTerm {
				rf.leaderReady = true
			}
			break
		}
	}
}

// ---- gRPC client helpers ----

func (rf *Raft) getConn(server int) (*grpc.ClientConn, error) {
	rf.mu.Lock()
	if conn, ok := rf.conns[server]; ok {
		rf.mu.Unlock()
		return conn, nil
	}
	addr := rf.peerAddrs[server]
	rf.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), rf.rpcTimeout)
	defer cancel()
	conn, err := grpc.DialContext(
		ctx,
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.CallContentSubtype("json")),
	)
	if err != nil {
		return nil, err
	}

	rf.mu.Lock()
	rf.conns[server] = conn
	rf.mu.Unlock()
	return conn, nil
}

func (rf *Raft) sendRequestVote(server int, args *RequestVoteArgs, reply *RequestVoteReply) bool {
	conn, err := rf.getConn(server)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), rf.rpcTimeout)
	defer cancel()
	out := &requestVoteRPCReply{}
	if err := conn.Invoke(ctx, "/coord.Raft/RequestVote", toRequestVoteRPC(args), out); err != nil {
		return false
	}
	reply.Term = out.Term
	reply.VoteGranted = out.VoteGranted
	return true
}

func (rf *Raft) sendAppendEntries(server int, args *AppendEntriesArgs, reply *AppendEntriesReply) bool {
	conn, err := rf.getConn(server)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), rf.rpcTimeout)
	defer cancel()
	out := &appendEntriesRPCReply{}
	if err := conn.Invoke(ctx, "/coord.Raft/AppendEntries", toAppendEntriesRPC(args), out); err != nil {
		return false
	}
	reply.Term = out.Term
	reply.Success = out.Success
	return true
}

// ---- gRPC service (handwritten, no proto) ----

type raftRPCService interface {
	RequestVote(context.Context, *requestVoteRPCRequest) (*requestVoteRPCReply, error)
	AppendEntries(context.Context, *appendEntriesRPCRequest) (*appendEntriesRPCReply, error)
}

type raftRPCServer struct {
	rf *Raft
}

func (s *raftRPCServer) RequestVote(ctx context.Context, req *requestVoteRPCRequest) (*requestVoteRPCReply, error) {
	args := fromRequestVoteRPC(req)
	reply := &RequestVoteReply{}
	s.rf.RequestVote(args, reply)
	return &requestVoteRPCReply{Term: reply.Term, VoteGranted: reply.VoteGranted}, nil
}

func (s *raftRPCServer) AppendEntries(ctx context.Context, req *appendEntriesRPCRequest) (*appendEntriesRPCReply, error) {
	args := fromAppendEntriesRPC(req)
	reply := &AppendEntriesReply{}
	s.rf.AppendEntries(args, reply)
	return &appendEntriesRPCReply{Term: reply.Term, Success: reply.Success}, nil
}

type requestVoteRPCRequest struct {
	Term         int
	CandidateId  int
	LastLogIndex int
	LastLogTerm  int
}

type requestVoteRPCReply struct {
	Term        int
	VoteGranted bool
}

type appendEntriesRPCRequest struct {
	Term         int
	LeaderId     int
	PrevLogIndex int
	PrevLogTerm  int
	Entries      []LogEntry
	LeaderCommit int
}

type appendEntriesRPCReply struct {
	Term    int
	Success bool
}

func toRequestVoteRPC(in *RequestVoteArgs) *requestVoteRPCRequest {
	return &requestVoteRPCRequest{
		Term:         in.Term,
		CandidateId:  in.CandidateId,
		LastLogIndex: in.LastLogIndex,
		LastLogTerm:  in.LastLogTerm,
	}
}

func fromRequestVoteRPC(in *requestVoteRPCRequest) *RequestVoteArgs {
	return &RequestVoteArgs{
		Term:         in.Term,
		CandidateId:  in.CandidateId,
		LastLogIndex: in.LastLogIndex,
		LastLogTerm:  in.LastLogTerm,
	}
}

func toAppendEntriesRPC(in *AppendEntriesArgs) *appendEntriesRPCRequest {
	return &appendEntriesRPCRequest{
		Term:         in.Term,
		LeaderId:     in.LeaderId,
		PrevLogIndex: in.PrevLogIndex,
		PrevLogTerm:  in.PrevLogTerm,
		Entries:      in.Entries,
		LeaderCommit: in.LeaderCommit,
	}
}

func fromAppendEntriesRPC(in *appendEntriesRPCRequest) *AppendEntriesArgs {
	return &AppendEntriesArgs{
		Term:         in.Term,
		LeaderId:     in.LeaderId,
		PrevLogIndex: in.PrevLogIndex,
		PrevLogTerm:  in.PrevLogTerm,
		Entries:      in.Entries,
		LeaderCommit: in.LeaderCommit,
	}
}

func registerRaftService(s *grpc.Server, srv raftRPCService) {
	s.RegisterService(&grpc.ServiceDesc{
		ServiceName: "coord.Raft",
		HandlerType: (*raftRPCService)(nil),
		Methods: []grpc.MethodDesc{
			{MethodName: "RequestVote", Handler: requestVoteHandler},
			{MethodName: "AppendEntries", Handler: appendEntriesHandler},
		},
		Streams:  []grpc.StreamDesc{},
		Metadata: "coord-raft",
	}, srv)
}

func requestVoteHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(requestVoteRPCRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(raftRPCService).RequestVote(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/coord.Raft/RequestVote"}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(raftRPCService).RequestVote(ctx, req.(*requestVoteRPCRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func appendEntriesHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(appendEntriesRPCRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(raftRPCService).AppendEntries(ctx, in)
	}
	info := &grpc.UnaryServerInfo{Server: srv, FullMethod: "/coord.Raft/AppendEntries"}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(raftRPCService).AppendEntries(ctx, req.(*appendEntriesRPCRequest))
	}
	return interceptor(ctx, in, info, handler)
}
