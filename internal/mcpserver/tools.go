package mcpserver

const (
	ToolCatalogQuery      = "catalog_query"
	ToolWorkspaceSearch   = "workspace_search"
	ToolPathExplain       = "path_explain"
	ToolDataQuery         = "data_query"
	ToolProjectRecords    = "project_records"
	ToolCheckpointWrite   = "checkpoint_write"
	ToolMemoWrite         = "memo_write"
	ToolCheckReportImport = "check_report_import"
	ToolRunPrepare        = "run_prepare"
	ToolRunExecute        = "run_execute"
	ToolRunInspect        = "run_inspect"
	ToolRuntimeAccess     = "runtime_access"
	ToolRuntimeConfig     = "runtime_config"
	ToolMemoryBackup      = "memory_backup"
)

const (
	ScopeRead          = "repoplane.read"
	ScopeIntentWrite   = "repoplane.intent.write"
	ScopeReportImport  = "repoplane.report.import"
	ScopeRunnerExecute = "repoplane.runner.execute"
	ScopeStateExport   = "repoplane.state.export"
)

// RequiredScope is the single source for the external tool authorization map.
// Unknown tools fail closed.
func RequiredScope(tool string) (string, bool) {
	switch tool {
	case ToolCatalogQuery, ToolWorkspaceSearch, ToolPathExplain, ToolDataQuery, ToolProjectRecords, ToolRuntimeAccess:
		return ScopeRead, true
	case ToolMemoryBackup, ToolRuntimeConfig:
		return ScopeStateExport, true
	case ToolCheckpointWrite, ToolMemoWrite:
		return ScopeIntentWrite, true
	case ToolCheckReportImport:
		return ScopeReportImport, true
	case ToolRunPrepare, ToolRunExecute, ToolRunInspect:
		return ScopeRunnerExecute, true
	default:
		return "", false
	}
}

// ToolNames returns the stable public tool names in authorization classes.
func ToolNames() (reads, writes, imports, runner, state []string) {
	return []string{ToolCatalogQuery, ToolWorkspaceSearch, ToolPathExplain, ToolDataQuery, ToolProjectRecords, ToolRuntimeAccess},
		[]string{ToolCheckpointWrite, ToolMemoWrite},
		[]string{ToolCheckReportImport},
		[]string{ToolRunPrepare, ToolRunExecute, ToolRunInspect},
		[]string{ToolMemoryBackup, ToolRuntimeConfig}
}
