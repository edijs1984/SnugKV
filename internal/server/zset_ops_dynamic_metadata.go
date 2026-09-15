package server

// ZMPOP has movable keys and a four-argument minimum form:
// ZMPOP numkeys key [key ...] MIN|MAX. Package variable dependencies ensure
// zsetOpsCommands is initialized before this adjustment, and all init functions
// subsequently publish the corrected metadata into commandTable.
var zsetOpsDynamicMetadata = func() struct{} {
	zsetOpsCommands["ZMPOP"] = commandInfo{4, 0, 0, 0, 0, true}
	return struct{}{}
}()
