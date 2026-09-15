package server

func init() {
	blocking := map[string]commandInfo{
		"BLPOP":       {3, 0, 1, -2, 1, true},
		"BRPOP":       {3, 0, 1, -2, 1, true},
		"BLMOVE":      {6, 6, 1, 2, 1, true},
		"BRPOPLPUSH": {4, 4, 1, 2, 1, true},
	}
	for name, info := range blocking {
		listCommands[name] = info
		commandTable[name] = info
	}
}
