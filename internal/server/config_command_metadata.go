package server

func init() {
	commandParentMetadataTable["CONFIG"] = commandParentMetadata{
		arity: -2,
		acl: []string{
			"@slow",
		},
		subcommands: []string{
			"CONFIG|RESETSTAT",
			"CONFIG|GET",
			"CONFIG|SET",
			"CONFIG|HELP",
			"CONFIG|REWRITE",
		},
	}

	adminFlags := []string{
		"admin",
		"noscript",
		"loading",
		"stale",
	}

	adminACL := []string{
		"@admin",
		"@slow",
		"@dangerous",
	}

	allNodesTips := []string{
		"request_policy:all_nodes",
		"response_policy:all_succeeded",
	}

	commandLeafMetadataTable["CONFIG|RESETSTAT"] =
		commandLeafMetadata{
			arity: 2,
			flags: append(
				[]string(nil),
				adminFlags...,
			),
			acl: append(
				[]string(nil),
				adminACL...,
			),
			tips: append(
				[]string(nil),
				allNodesTips...,
			),
		}

	commandLeafMetadataTable["CONFIG|GET"] =
		commandLeafMetadata{
			arity: -3,
			flags: append(
				[]string(nil),
				adminFlags...,
			),
			acl: append(
				[]string(nil),
				adminACL...,
			),
		}

	commandLeafMetadataTable["CONFIG|SET"] =
		commandLeafMetadata{
			arity: -4,
			flags: append(
				[]string(nil),
				adminFlags...,
			),
			acl: append(
				[]string(nil),
				adminACL...,
			),
			tips: append(
				[]string(nil),
				allNodesTips...,
			),
		}

	commandLeafMetadataTable["CONFIG|HELP"] =
		commandLeafMetadata{
			arity: 2,
			flags: []string{
				"loading",
				"stale",
			},
			acl: []string{
				"@slow",
			},
		}

	commandLeafMetadataTable["CONFIG|REWRITE"] =
		commandLeafMetadata{
			arity: 2,
			flags: append(
				[]string(nil),
				adminFlags...,
			),
			acl: append(
				[]string(nil),
				adminACL...,
			),
			tips: append(
				[]string(nil),
				allNodesTips...,
			),
		}

	commandContainerDocs["CONFIG"] = commandContainerDoc{
		summary:    "A container for server configuration commands.",
		since:      "2.0.0",
		group:      "server",
		complexity: "Depends on subcommand.",
		subcommands: []string{
			"CONFIG|RESETSTAT",
			"CONFIG|GET",
			"CONFIG|SET",
			"CONFIG|HELP",
			"CONFIG|REWRITE",
		},
	}

	commandSubcommandDocs["CONFIG|RESETSTAT"] =
		commandDoc{
			summary:    "Resets the server's statistics.",
			since:      "2.0.0",
			group:      "server",
			complexity: "O(1)",
		}

	commandSubcommandDocs["CONFIG|GET"] =
		commandDoc{
			summary:    "Returns the effective values of configuration parameters.",
			since:      "2.0.0",
			group:      "server",
			complexity: "O(N) when N is the number of configuration parameters provided",
			history: [][2]string{
				{
					"7.0.0",
					"Added the ability to pass multiple pattern parameters in one call",
				},
			},
			arguments: []commandDocArgument{
				{
					name:        "parameter",
					displayText: "parameter",
					argType:     "string",
					multiple:    true,
				},
			},
		}

	commandSubcommandDocs["CONFIG|SET"] =
		commandDoc{
			summary:    "Sets configuration parameters in-flight.",
			since:      "2.0.0",
			group:      "server",
			complexity: "O(N) when N is the number of configuration parameters provided",
			history: [][2]string{
				{
					"7.0.0",
					"Added the ability to set multiple parameters in one call.",
				},
			},
			arguments: []commandDocArgument{
				{
					name:     "data",
					argType:  "block",
					multiple: true,
					arguments: []commandDocArgument{
						{
							name:        "parameter",
							displayText: "parameter",
							argType:     "string",
						},
						{
							name:        "value",
							displayText: "value",
							argType:     "string",
						},
					},
				},
			},
		}

	commandSubcommandDocs["CONFIG|HELP"] =
		commandDoc{
			summary:    "Returns helpful text about the different subcommands.",
			since:      "5.0.0",
			group:      "server",
			complexity: "O(1)",
		}

	commandSubcommandDocs["CONFIG|REWRITE"] =
		commandDoc{
			summary:    "Persists the effective configuration to file.",
			since:      "2.8.0",
			group:      "server",
			complexity: "O(1)",
		}
}
