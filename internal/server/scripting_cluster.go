package server

import (
	"errors"

	lua "github.com/yuin/gopher-lua"
)

const (
	luaClusterAllowCrossSlotGlobal = "__snug_cluster_allow_cross_slot"
	luaClusterRuntimeSlotGlobal    = "__snug_cluster_runtime_slot"
)

func (s *Server) prepareLuaClusterScope(L *lua.LState, allowCrossSlot bool) {
	if !s.clusterEnabled {
		return
	}
	L.SetGlobal(luaClusterAllowCrossSlotGlobal, lua.LBool(allowCrossSlot))
	L.SetGlobal(luaClusterRuntimeSlotGlobal, lua.LNumber(-1))
}

func (s *Server) validateLuaClusterAccess(L *lua.LState, args [][]byte) error {
	if !s.clusterEnabled {
		return nil
	}

	refs, err := commandKeys(args)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}

	allowCrossSlot := false
	if value := L.GetGlobal(luaClusterAllowCrossSlotGlobal); value != lua.LNil {
		if enabled, ok := value.(lua.LBool); ok {
			allowCrossSlot = bool(enabled)
		}
	}

	runtimeSlot := -1
	if value := L.GetGlobal(luaClusterRuntimeSlotGlobal); value != lua.LNil {
		if slot, ok := value.(lua.LNumber); ok {
			runtimeSlot = int(slot)
		}
	}

	for _, ref := range refs {
		slot := clusterKeySlot(ref.value)
		owner := s.clusterSlotOwners[slot]
		if owner == "" {
			return errors.New("CLUSTERDOWN Hash slot not served")
		}
		if owner != s.clusterNodeAddr {
			return errors.New("ERR Script attempted to access a non local key in a cluster node")
		}

		if allowCrossSlot {
			continue
		}
		if runtimeSlot < 0 {
			runtimeSlot = slot
			L.SetGlobal(luaClusterRuntimeSlotGlobal, lua.LNumber(slot))
			continue
		}
		if slot != runtimeSlot {
			return errors.New("ERR Script attempted to access a non local key in a cluster node")
		}
	}

	return nil
}
