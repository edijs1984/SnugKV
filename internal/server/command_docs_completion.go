package server

func init() {
	// -------------------------------------------------------------------------
	// BITMAP
	// -------------------------------------------------------------------------

	commandDocs["BITOP"] = commandDoc{
		summary:    "Performs bitwise operations on multiple strings, and stores the result.",
		since:      "2.6.0",
		group:      "bitmap",
		complexity: "O(N)",
		arguments: []commandDocArgument{
			{
				name:    "operation",
				argType: "oneof",
				arguments: []commandDocArgument{
					pureToken("and", "AND"),
					pureToken("or", "OR"),
					pureToken("xor", "XOR"),
					pureToken("not", "NOT"),
					pureToken("diff", "DIFF"),
					pureToken("diff1", "DIFF1"),
					pureToken("andor", "ANDOR"),
					pureToken("one", "ONE"),
				},
			},
			{
				name:         "destkey",
				displayText:  "destkey",
				argType:      "key",
				keySpecIndex: 0,
				hasKeySpec:   true,
			},
			{
				name:         "key",
				displayText:  "key",
				argType:      "key",
				keySpecIndex: 1,
				hasKeySpec:   true,
				multiple:     true,
			},
		},
	}

	// -------------------------------------------------------------------------
	// SCRIPTING
	// -------------------------------------------------------------------------

	commandDocs["EVALSHA"] = commandDoc{
		summary:    "Executes a server-side Lua script by SHA1 digest.",
		since:      "2.6.0",
		group:      "scripting",
		complexity: "Depends on the script that is executed.",
		arguments:  scriptingCommandDocArguments("sha1"),
	}

	commandDocs["EVAL_RO"] = commandDoc{
		summary:    "Executes a read-only server-side Lua script.",
		since:      "7.0.0",
		group:      "scripting",
		complexity: "Depends on the script that is executed.",
		arguments:  scriptingCommandDocArguments("script"),
	}

	commandDocs["EVALSHA_RO"] = commandDoc{
		summary:    "Executes a read-only server-side Lua script by SHA1 digest.",
		since:      "7.0.0",
		group:      "scripting",
		complexity: "Depends on the script that is executed.",
		arguments:  scriptingCommandDocArguments("sha1"),
	}

	commandDocs["FCALL_RO"] = commandDoc{
		summary:    "Invokes a read-only function.",
		since:      "7.0.0",
		group:      "scripting",
		complexity: "Depends on the function that is executed.",
		arguments:  functionCallDocArguments(),
	}

	// -------------------------------------------------------------------------
	// SORT
	// -------------------------------------------------------------------------

	commandDocs["SORT"] = commandDoc{
		summary:    "Sorts the elements in a list, a set, or a sorted set, optionally storing the result.",
		since:      "1.0.0",
		group:      "generic",
		complexity: "O(N+M*log(M)) where N is the number of elements in the list or set to sort, and M the number of returned elements. When the elements are not sorted, complexity is O(N).",
		arguments:  sortCommandDocArguments(true),
	}

	commandDocs["SORT_RO"] = commandDoc{
		summary:    "Returns the sorted elements of a list, a set, or a sorted set.",
		since:      "7.0.0",
		group:      "generic",
		complexity: "O(N+M*log(M)) where N is the number of elements in the list or set to sort, and M the number of returned elements. When the elements are not sorted, complexity is O(N).",
		arguments:  sortCommandDocArguments(false),
	}

	// -------------------------------------------------------------------------
	// SORTED SET ALGEBRA
	// -------------------------------------------------------------------------

	commandDocs["ZUNION"] = zsetReadDoc(
		"Returns the union of multiple sorted sets.",
		"6.2.0",
		"O(N)+O(M*log(M)) with N being the sum of the sizes of the input sorted sets, and M being the number of elements in the resulting sorted set.",
		true,
		true,
	)

	commandDocs["ZINTER"] = zsetReadDoc(
		"Returns the intersect of multiple sorted sets.",
		"6.2.0",
		"O(N*K)+O(M*log(M)) worst case with N being the smallest input sorted set, K being the number of input sorted sets and M being the number of elements in the resulting sorted set.",
		true,
		true,
	)

	commandDocs["ZDIFF"] = zsetReadDoc(
		"Returns the difference between multiple sorted sets.",
		"6.2.0",
		"O(L + (N-K)log(N)) worst case where L is the total number of elements in all the sets, N is the size of the first set, and K is the size of the result set.",
		false,
		true,
	)

	commandDocs["ZINTERCARD"] = commandDoc{
		summary:    "Returns the number of members of the intersect of multiple sorted sets.",
		since:      "7.0.0",
		group:      "sorted-set",
		complexity: "O(N*K) worst case with N being the smallest input sorted set, K being the number of input sorted sets.",
		arguments: append(
			zsetNumKeysArgs(0),
			commandDocArgument{
				name:        "limit",
				displayText: "limit",
				argType:     "integer",
				token:       "LIMIT",
				optional:    true,
			},
		),
	}

	commandDocs["ZUNIONSTORE"] = zsetStoreDoc(
		"Stores the union of multiple sorted sets in a key.",
		"2.0.0",
		"O(N)+O(M log(M)) with N being the sum of the sizes of the input sorted sets, and M being the number of elements in the resulting sorted set.",
		true,
	)

	commandDocs["ZINTERSTORE"] = zsetStoreDoc(
		"Stores the intersect of multiple sorted sets in a key.",
		"2.0.0",
		"O(N*K)+O(M*log(M)) worst case with N being the smallest input sorted set, K being the number of input sorted sets and M being the number of elements in the resulting sorted set.",
		true,
	)

	commandDocs["ZDIFFSTORE"] = zsetStoreDoc(
		"Stores the difference of multiple sorted sets in a key.",
		"6.2.0",
		"O(L + (N-K)log(N)) worst case where L is the total number of elements in all the sets, N is the size of the first set, and K is the size of the result set.",
		false,
	)

	commandDocs["ZMPOP"] = zmpopDoc(false)
	commandDocs["BZMPOP"] = zmpopDoc(true)

	// -------------------------------------------------------------------------
	// STREAMS
	// -------------------------------------------------------------------------

	commandDocs["XREAD"] = commandDoc{
		summary: "Returns messages from multiple streams with IDs greater than the ones requested. Blocks until a message is available otherwise.",
		since:   "5.0.0",
		group:   "stream",
		arguments: []commandDocArgument{
			{
				name:        "count",
				displayText: "count",
				argType:     "integer",
				token:       "COUNT",
				optional:    true,
			},
			{
				name:        "milliseconds",
				displayText: "milliseconds",
				argType:     "integer",
				token:       "BLOCK",
				optional:    true,
			},
			streamBlockDocArgument(),
		},
	}

	commandDocs["XREADGROUP"] = commandDoc{
		summary:    "Returns new or historical messages from a stream for a consumer in a group. Blocks until a message is available otherwise.",
		since:      "5.0.0",
		group:      "stream",
		complexity: "For each stream mentioned: O(M) with M being the number of elements returned. If M is constant (e.g. always asking for the first 10 elements with COUNT), you can consider it O(1). On the other side when XREADGROUP blocks, XADD will pay the O(N) time in order to serve the N clients blocked on the stream getting new data.",
		arguments: []commandDocArgument{
			{
				name:    "group-block",
				argType: "block",
				token:   "GROUP",
				arguments: []commandDocArgument{
					{
						name:        "group",
						displayText: "group",
						argType:     "string",
					},
					{
						name:        "consumer",
						displayText: "consumer",
						argType:     "string",
					},
				},
			},
			{
				name:        "count",
				displayText: "count",
				argType:     "integer",
				token:       "COUNT",
				optional:    true,
			},
			{
				name:        "milliseconds",
				displayText: "milliseconds",
				argType:     "integer",
				token:       "BLOCK",
				optional:    true,
			},
			{
				name:        "noack",
				displayText: "noack",
				argType:     "pure-token",
				token:       "NOACK",
				optional:    true,
			},
			streamBlockDocArgument(),
		},
	}

	// Redis 8.2 additions already supported by SnugKV.
	xadd := commandDocs["XADD"]
	xadd.history = append(
		xadd.history,
		[2]string{
			"8.2.0",
			"Added the `KEEPREF`, `DELREF` and `ACKED` options.",
		},
	)

	if len(xadd.arguments) >= 2 {
		condition := commandDocArgument{
			name:     "condition",
			argType:  "oneof",
			optional: true,
			arguments: []commandDocArgument{
				pureToken("keepref", "KEEPREF"),
				pureToken("delref", "DELREF"),
				pureToken("acked", "ACKED"),
			},
		}

		// Insert after NOMKSTREAM and before trim.
		args := make([]commandDocArgument, 0, len(xadd.arguments)+1)
		args = append(args, xadd.arguments[:2]...)
		args = append(args, condition)
		args = append(args, xadd.arguments[2:]...)
		xadd.arguments = args
	}

	commandDocs["XADD"] = xadd
}

func scriptingCommandDocArguments(firstName string) []commandDocArgument {
	return []commandDocArgument{
		{
			name:        firstName,
			displayText: firstName,
			argType:     "string",
		},
		{
			name:        "numkeys",
			displayText: "numkeys",
			argType:     "integer",
		},
		{
			name:         "key",
			displayText:  "key",
			argType:      "key",
			keySpecIndex: 0,
			hasKeySpec:   true,
			optional:     true,
			multiple:     true,
		},
		{
			name:        "arg",
			displayText: "arg",
			argType:     "string",
			optional:    true,
			multiple:    true,
		},
	}
}

func functionCallDocArguments() []commandDocArgument {
	return []commandDocArgument{
		{
			name:        "function",
			displayText: "function",
			argType:     "string",
		},
		{
			name:        "numkeys",
			displayText: "numkeys",
			argType:     "integer",
		},
		{
			name:         "key",
			displayText:  "key",
			argType:      "key",
			keySpecIndex: 0,
			hasKeySpec:   true,
			optional:     true,
			multiple:     true,
		},
		{
			name:        "arg",
			displayText: "arg",
			argType:     "string",
			optional:    true,
			multiple:    true,
		},
	}
}

func sortCommandDocArguments(withStore bool) []commandDocArgument {
	args := []commandDocArgument{
		{
			name:         "key",
			displayText:  "key",
			argType:      "key",
			keySpecIndex: 0,
			hasKeySpec:   true,
		},
		{
			name:         "by-pattern",
			displayText:  "pattern",
			argType:      "pattern",
			token:        "BY",
			keySpecIndex: 1,
			hasKeySpec:   true,
			optional:     true,
		},
		{
			name:     "limit",
			argType:  "block",
			token:    "LIMIT",
			optional: true,
			arguments: []commandDocArgument{
				{
					name:        "offset",
					displayText: "offset",
					argType:     "integer",
				},
				{
					name:        "count",
					displayText: "count",
					argType:     "integer",
				},
			},
		},
		{
			name:         "get-pattern",
			displayText:  "pattern",
			argType:      "pattern",
			token:        "GET",
			keySpecIndex: 1,
			hasKeySpec:   true,
			optional:     true,
			multiple:     true,
		},
		{
			name:     "order",
			argType:  "oneof",
			optional: true,
			arguments: []commandDocArgument{
				pureToken("asc", "ASC"),
				pureToken("desc", "DESC"),
			},
		},
		{
			name:        "sorting",
			displayText: "sorting",
			argType:     "pure-token",
			token:       "ALPHA",
			optional:    true,
		},
	}

	if withStore {
		args = append(
			args,
			commandDocArgument{
				name:         "destination",
				displayText:  "destination",
				argType:      "key",
				token:        "STORE",
				keySpecIndex: 2,
				hasKeySpec:   true,
				optional:     true,
			},
		)
	}

	return args
}

func zsetNumKeysArgs(keySpecIndex int) []commandDocArgument {
	return []commandDocArgument{
		{
			name:        "numkeys",
			displayText: "numkeys",
			argType:     "integer",
		},
		{
			name:         "key",
			displayText:  "key",
			argType:      "key",
			keySpecIndex: keySpecIndex,
			hasKeySpec:   true,
			multiple:     true,
		},
	}
}

func zsetWeightsArgument() commandDocArgument {
	return commandDocArgument{
		name:        "weight",
		displayText: "weight",
		argType:     "integer",
		token:       "WEIGHTS",
		optional:    true,
		multiple:    true,
	}
}

func zsetAggregateArgument() commandDocArgument {
	return commandDocArgument{
		name:     "aggregate",
		argType:  "oneof",
		token:    "AGGREGATE",
		optional: true,
		arguments: []commandDocArgument{
			pureToken("sum", "SUM"),
			pureToken("min", "MIN"),
			pureToken("max", "MAX"),
		},
	}
}

func zsetReadDoc(
	summary string,
	since string,
	complexity string,
	withWeights bool,
	withScores bool,
) commandDoc {
	args := zsetNumKeysArgs(0)

	if withWeights {
		args = append(
			args,
			zsetWeightsArgument(),
			zsetAggregateArgument(),
		)
	}

	if withScores {
		args = append(
			args,
			commandDocArgument{
				name:        "withscores",
				displayText: "withscores",
				argType:     "pure-token",
				token:       "WITHSCORES",
				optional:    true,
			},
		)
	}

	return commandDoc{
		summary:    summary,
		since:      since,
		group:      "sorted-set",
		complexity: complexity,
		arguments:  args,
	}
}

func zsetStoreDoc(
	summary string,
	since string,
	complexity string,
	withWeights bool,
) commandDoc {
	args := []commandDocArgument{
		{
			name:         "destination",
			displayText:  "destination",
			argType:      "key",
			keySpecIndex: 0,
			hasKeySpec:   true,
		},
		{
			name:        "numkeys",
			displayText: "numkeys",
			argType:     "integer",
		},
		{
			name:         "key",
			displayText:  "key",
			argType:      "key",
			keySpecIndex: 1,
			hasKeySpec:   true,
			multiple:     true,
		},
	}

	if withWeights {
		args = append(
			args,
			zsetWeightsArgument(),
			zsetAggregateArgument(),
		)
	}

	return commandDoc{
		summary:    summary,
		since:      since,
		group:      "sorted-set",
		complexity: complexity,
		arguments:  args,
	}
}

func zmpopDoc(blocking bool) commandDoc {
	args := make([]commandDocArgument, 0, 5)

	if blocking {
		args = append(
			args,
			commandDocArgument{
				name:        "timeout",
				displayText: "timeout",
				argType:     "double",
			},
		)
	}

	args = append(
		args,
		commandDocArgument{
			name:        "numkeys",
			displayText: "numkeys",
			argType:     "integer",
		},
		commandDocArgument{
			name:         "key",
			displayText:  "key",
			argType:      "key",
			keySpecIndex: 0,
			hasKeySpec:   true,
			multiple:     true,
		},
		commandDocArgument{
			name:    "where",
			argType: "oneof",
			arguments: []commandDocArgument{
				pureToken("min", "MIN"),
				pureToken("max", "MAX"),
			},
		},
		commandDocArgument{
			name:        "count",
			displayText: "count",
			argType:     "integer",
			token:       "COUNT",
			optional:    true,
		},
	)

	summary := "Returns the highest- or lowest-scoring members from one or more sorted sets after removing them. Deletes the sorted set if the last member was popped."

	if blocking {
		summary = "Removes and returns a member by score from one or more sorted sets. Blocks until a member is available otherwise. Deletes the sorted set if the last element was popped."
	}

	return commandDoc{
		summary:    summary,
		since:      "7.0.0",
		group:      "sorted-set",
		complexity: "O(K) + O(M*log(N)) where K is the number of provided keys, N being the number of elements in the sorted set, and M being the number of elements popped.",
		arguments:  args,
	}
}

func streamBlockDocArgument() commandDocArgument {
	return commandDocArgument{
		name:    "streams",
		argType: "block",
		token:   "STREAMS",
		arguments: []commandDocArgument{
			{
				name:         "key",
				displayText:  "key",
				argType:      "key",
				keySpecIndex: 0,
				hasKeySpec:   true,
				multiple:     true,
			},
			{
				name:        "id",
				displayText: "id",
				argType:     "string",
				multiple:    true,
			},
		},
	}
}
