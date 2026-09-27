package server

import (
	"encoding/json"
	"errors"
	"time"
)

type failoverTopologyView struct {
	GroupID        string                   `json:"group_id,omitempty"`
	ConfigEpoch    uint64                   `json:"config_epoch,omitempty"`
	AdvertiseAddr  string                   `json:"advertise_addr,omitempty"`
	Members        []string                 `json:"members,omitempty"`
	Quorum         int                      `json:"quorum,omitempty"`
	JointActive    bool                     `json:"joint_active,omitempty"`
	PendingEpoch   uint64                   `json:"pending_epoch,omitempty"`
	PendingMembers []string                 `json:"pending_members,omitempty"`
	PendingQuorum  int                      `json:"pending_quorum,omitempty"`
	CommitPending  bool                     `json:"commit_pending,omitempty"`
	CommitEpoch    uint64                   `json:"commit_epoch,omitempty"`
	Retired        bool                     `json:"retired,omitempty"`
	RetiredAtEpoch uint64                   `json:"retired_at_epoch,omitempty"`
	Discovered     []failoverDiscoveredPeer `json:"discovered,omitempty"`
}

type failoverHealthPeer struct {
	Address   string `json:"address"`
	Reachable bool   `json:"reachable"`
	NodeID    string `json:"node_id,omitempty"`
	Role      string `json:"role,omitempty"`
	Retired   bool   `json:"retired,omitempty"`
}

type failoverHealthView struct {
	Status             string               `json:"status"`
	GroupID            string               `json:"group_id,omitempty"`
	ConfigEpoch        uint64               `json:"config_epoch,omitempty"`
	Role               string               `json:"role"`
	MasterLinkStatus   string               `json:"master_link_status,omitempty"`
	MasterRunID        string               `json:"master_run_id,omitempty"`
	Quorum             int                  `json:"quorum"`
	ReachableVoters    int                  `json:"reachable_voters"`
	QuorumReachable    bool                 `json:"quorum_reachable"`
	JointActive        bool                 `json:"joint_active,omitempty"`
	CommitPending      bool                 `json:"commit_pending,omitempty"`
	Retired            bool                 `json:"retired,omitempty"`
	LeaderActive       bool                 `json:"leader_active,omitempty"`
	LeaderTerm         uint64               `json:"leader_term,omitempty"`
	LeaderID           string               `json:"leader_id,omitempty"`
	LeaderLeaseUntilMS int64                `json:"leader_lease_until_ms,omitempty"`
	WriteFenced        bool                 `json:"write_fenced,omitempty"`
	Peers              []failoverHealthPeer `json:"peers,omitempty"`
}

func (s *Server) failoverTopology(now time.Time) failoverTopologyView {
	m := s.failoverMembershipSnapshot()
	members := make([]string, 0, len(m.Peers)+1)
	if s.failoverAdvertiseAddr != "" {
		members = append(members, s.failoverAdvertiseAddr)
	}
	members = append(members, m.Peers...)

	pending := make([]string, 0, len(m.PendingPeers)+1)
	if m.JointActive && s.failoverAdvertiseAddr != "" {
		pending = append(pending, s.failoverAdvertiseAddr)
	}
	pending = append(pending, m.PendingPeers...)

	return failoverTopologyView{
		GroupID:        m.GroupID,
		ConfigEpoch:    m.ConfigEpoch,
		AdvertiseAddr:  s.failoverAdvertiseAddr,
		Members:        members,
		Quorum:         m.Quorum,
		JointActive:    m.JointActive,
		PendingEpoch:   m.PendingEpoch,
		PendingMembers: pending,
		PendingQuorum:  m.PendingQuorum,
		CommitPending:  m.CommitPending,
		CommitEpoch:    m.CommitEpoch,
		Retired:        m.Retired,
		RetiredAtEpoch: m.RetiredAtEpoch,
		Discovered:     s.discoveredFailoverPeersAt(now),
	}
}

func (s *Server) failoverTopologyJSON(now time.Time) ([]byte, error) {
	return json.Marshal(s.failoverTopology(now))
}

func (s *Server) failoverHealth(now time.Time) failoverHealthView {
	m := s.failoverMembershipSnapshot()
	local := s.localFailoverState(now)
	rep := s.replication.snapshot()
	active, term, _, leaderID, leaseUntil, fenced := s.failoverLeaderState()

	view := failoverHealthView{
		Status:           "healthy",
		GroupID:          m.GroupID,
		ConfigEpoch:      m.ConfigEpoch,
		Role:             local.Role,
		MasterLinkStatus: rep.masterLinkStatus,
		MasterRunID:      local.MasterRunID,
		Quorum:           m.Quorum,
		JointActive:      m.JointActive,
		CommitPending:    m.CommitPending,
		Retired:          m.Retired,
		LeaderActive:     active,
		LeaderTerm:       term,
		LeaderID:         leaderID,
		WriteFenced:      fenced || s.failoverWritesFenced(now),
	}
	if !leaseUntil.IsZero() {
		view.LeaderLeaseUntilMS = leaseUntil.UnixMilli()
	}

	if !m.Retired {
		view.ReachableVoters = 1
	}
	for _, addr := range m.Peers {
		entry := failoverHealthPeer{Address: addr}
		peer, err := queryFailoverPeer(addr, 200*time.Millisecond, s.replicationMasterUser, s.replicationMasterAuth)
		if err == nil && s.failoverPeerMembershipMatches(peer) {
			entry.Reachable = true
			entry.NodeID = peer.NodeID
			entry.Role = peer.Role
			entry.Retired = peer.Retired
			if !peer.Retired {
				view.ReachableVoters++
			}
		}
		view.Peers = append(view.Peers, entry)
	}
	view.QuorumReachable = m.Quorum == 0 || view.ReachableVoters >= m.Quorum

	switch {
	case m.Retired:
		view.Status = "retired"
	case view.WriteFenced:
		view.Status = "fenced"
	case m.JointActive || m.CommitPending:
		view.Status = "transitioning"
	case !view.QuorumReachable:
		view.Status = "degraded"
	case local.Role == "replica" && rep.masterLinkStatus == "down":
		view.Status = "degraded"
	}
	return view
}

func (s *Server) failoverHealthJSON(now time.Time) ([]byte, error) {
	return json.Marshal(s.failoverHealth(now))
}


type failoverTransitionTargetStatus struct {
	Address            string `json:"address"`
	Kind               string `json:"kind"`
	Reachable          bool   `json:"reachable"`
	GroupID            string `json:"group_id,omitempty"`
	ConfigEpoch        uint64 `json:"config_epoch,omitempty"`
	JointActive        bool   `json:"joint_active,omitempty"`
	PendingEpoch       uint64 `json:"pending_epoch,omitempty"`
	Retired            bool   `json:"retired,omitempty"`
	RetiredAtEpoch     uint64 `json:"retired_at_epoch,omitempty"`
	RetirePending      bool   `json:"retire_pending,omitempty"`
	RetirePendingEpoch uint64 `json:"retire_pending_epoch,omitempty"`
	Converged          bool   `json:"converged"`
}

type failoverTransitionDiagnosis struct {
	Active          bool                             `json:"active"`
	Phase           string                           `json:"phase,omitempty"`
	Blocker         string                           `json:"blocker,omitempty"`
	GroupID         string                           `json:"group_id,omitempty"`
	CurrentEpoch    uint64                           `json:"current_epoch,omitempty"`
	PendingEpoch    uint64                           `json:"pending_epoch,omitempty"`
	CommitEpoch     uint64                           `json:"commit_epoch,omitempty"`
	CommitPending   bool                             `json:"commit_pending,omitempty"`
	CanRetry        bool                             `json:"can_retry"`
	Targets         []failoverTransitionTargetStatus `json:"targets,omitempty"`
}

func (s *Server) diagnoseFailoverTransition(now time.Time) failoverTransitionDiagnosis {
	m := s.failoverMembershipSnapshot()
	d := failoverTransitionDiagnosis{
		GroupID:       m.GroupID,
		CurrentEpoch:  m.ConfigEpoch,
		PendingEpoch:  m.PendingEpoch,
		CommitEpoch:   m.CommitEpoch,
		CommitPending: m.CommitPending,
		CanRetry:      m.CommitPending,
	}
	if !m.JointActive && !m.CommitPending {
		d.Phase = "idle"
		return d
	}
	d.Active = true
	if m.CommitPending {
		d.Phase = "commit-forward"
	} else {
		d.Phase = "prepared"
		d.Blocker = "commit intent has not been persisted; inspect quorum and coordinator state"
	}

	retireSet := make(map[string]struct{}, len(m.CommitRetireTargets))
	for _, addr := range m.CommitRetireTargets {
		retireSet[addr] = struct{}{}
	}

	for _, addr := range unionStrings(m.CommitTargets, m.CommitRetireTargets) {
		kind := "commit"
		if _, ok := retireSet[addr]; ok {
			kind = "retire"
		}
		target := failoverTransitionTargetStatus{Address: addr, Kind: kind}
		if addr == s.failoverAdvertiseAddr {
			local := s.localFailoverState(now)
			target.Reachable = true
			target.GroupID = local.GroupID
			target.ConfigEpoch = local.ConfigEpoch
			target.JointActive = local.JointActive
			target.PendingEpoch = local.PendingEpoch
			target.Retired = local.Retired
			target.RetiredAtEpoch = local.RetiredAtEpoch
			target.RetirePending = local.RetirePending
			target.RetirePendingEpoch = local.RetirePendingEpoch
		} else {
			state, err := queryFailoverPeer(
				addr,
				200*time.Millisecond,
				s.replicationMasterUser,
				s.replicationMasterAuth,
			)
			if err == nil {
				target.Reachable = true
				target.GroupID = state.GroupID
				target.ConfigEpoch = state.ConfigEpoch
				target.JointActive = state.JointActive
				target.PendingEpoch = state.PendingEpoch
				target.Retired = state.Retired
				target.RetiredAtEpoch = state.RetiredAtEpoch
				target.RetirePending = state.RetirePending
				target.RetirePendingEpoch = state.RetirePendingEpoch
			}
		}
		if kind == "retire" {
			target.Converged = target.Reachable &&
				target.GroupID == m.GroupID &&
				target.Retired &&
				target.RetiredAtEpoch == m.CommitEpoch
		} else {
			target.Converged = target.Reachable &&
				target.GroupID == m.GroupID &&
				target.ConfigEpoch >= m.CommitEpoch &&
				!target.JointActive
		}
		if d.Blocker == "" && !target.Converged {
			switch {
			case !target.Reachable:
				d.Blocker = "one or more transition targets are unreachable"
			case target.GroupID != m.GroupID:
				d.Blocker = "one or more transition targets report a different failover group"
			case kind == "retire" && !target.Retired:
				d.Blocker = "one or more removed members have not finalized retirement"
			default:
				d.Blocker = "one or more members have not converged to the committed epoch"
			}
		}
		d.Targets = append(d.Targets, target)
	}
	if d.Blocker == "" && m.CommitPending {
		d.Blocker = "recovery is pending final convergence bookkeeping"
	}
	return d
}

func (s *Server) retryFailoverTransitionNow(now time.Time) (failoverTransitionDiagnosis, error) {
	m := s.failoverMembershipSnapshot()
	if !m.CommitPending {
		return s.diagnoseFailoverTransition(now), errors.New("no durable failover membership commit is pending")
	}
	s.failoverMembershipMu.Lock()
	s.failoverCommitLastRetry = time.Time{}
	s.failoverMembershipMu.Unlock()
	if err := s.retryFailoverMembershipCommit(now); err != nil {
		return s.diagnoseFailoverTransition(now), err
	}
	return s.diagnoseFailoverTransition(now), nil
}

func (s *Server) failoverTransitionDiagnosisJSON(now time.Time) ([]byte, error) {
	return json.Marshal(s.diagnoseFailoverTransition(now))
}
