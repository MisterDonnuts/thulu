//nolint:lll // OpenAPI schema strings are inherently long; splitting them hurts readability
package api

import (
	"encoding/json"
	"net/http"

	"github.com/MisterDonnuts/thulu/internal/i18n"
	"github.com/MisterDonnuts/thulu/internal/settings"
)

// Hand-maintained; must change together with the routes in server.go.

type openAPISpec struct {
	OpenAPI    string                 `json:"openapi"`
	Info       openAPIInfo            `json:"info"`
	Servers    []openAPIServer        `json:"servers"`
	Paths      map[string]openAPIPath `json:"paths"`
	Components openAPIComponents      `json:"components"`
}

type openAPIInfo struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Version     string `json:"version"`
}

type openAPIServer struct {
	URL         string `json:"url"`
	Description string `json:"description"`
}

type openAPIPath map[string]openAPIOperation

type openAPIOperation struct {
	Tags        []string                   `json:"tags,omitempty"`
	Summary     string                     `json:"summary"`
	Description string                     `json:"description,omitempty"`
	OperationID string                     `json:"operationId"`
	Parameters  []openAPIParameter         `json:"parameters,omitempty"`
	RequestBody *openAPIRequestBody        `json:"requestBody,omitempty"`
	Responses   map[string]openAPIResponse `json:"responses"`
	Security    []map[string][]string      `json:"security,omitempty"`
}

type openAPIParameter struct {
	Name        string        `json:"name"`
	In          string        `json:"in"`
	Description string        `json:"description,omitempty"`
	Required    bool          `json:"required"`
	Schema      openAPISchema `json:"schema"`
}

type openAPIRequestBody struct {
	Required bool                      `json:"required"`
	Content  map[string]openAPIContent `json:"content"`
}

type openAPIContent struct {
	Schema openAPISchema `json:"schema"`
}

type openAPIResponse struct {
	Description string                    `json:"description"`
	Content     map[string]openAPIContent `json:"content,omitempty"`
}

type openAPISchema struct {
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`

	Format     string                   `json:"format,omitempty"`
	Ref        string                   `json:"$ref,omitempty"`
	Items      *openAPISchema           `json:"items,omitempty"`
	Properties map[string]openAPISchema `json:"properties,omitempty"`
}

type openAPIComponents struct {
	SecuritySchemes map[string]openAPISecurityScheme `json:"securitySchemes"`
	Schemas         map[string]openAPISchema         `json:"schemas"`
}

type openAPISecurityScheme struct {
	Type         string `json:"type"`
	Scheme       string `json:"scheme,omitempty"`
	BearerFormat string `json:"bearerFormat,omitempty"`
	In           string `json:"in,omitempty"`
	Name         string `json:"name,omitempty"`
}

const (
	schemaTask              = "Task"       //i18n:ignore OpenAPI schema name, an identifier
	schemaProject           = "Project"    //i18n:ignore OpenAPI schema name, an identifier
	schemaAgent             = "Agent"      //i18n:ignore OpenAPI schema name, an identifier
	schemaComment           = "Comment"    //i18n:ignore OpenAPI schema name, an identifier
	schemaAttachment        = "Attachment" //i18n:ignore OpenAPI schema name, an identifier
	schemaChat              = "Chat"       //i18n:ignore OpenAPI schema name, an identifier
	schemaChatMessage       = "ChatMessage"
	schemaFlag              = "Flag" //i18n:ignore OpenAPI schema name, an identifier
	schemaTestCase          = "TestCase"
	schemaReport            = "Report" //i18n:ignore OpenAPI schema name, an identifier
	schemaTestCaseRun       = "TestCaseRun"
	schemaWebhook           = "Webhook" //i18n:ignore OpenAPI schema name, an identifier
	schemaWebhookDelivery   = "WebhookDelivery"
	schemaAutomation        = "Automation" //i18n:ignore OpenAPI schema name, an identifier
	schemaAutomationRun     = "AutomationRun"
	schemaAuditEntry        = "AuditEntry"
	schemaAuditChainReport  = "AuditChainReport"
	schemaAuthConfig        = "AuthConfig"
	schemaUser              = "User" //i18n:ignore OpenAPI schema name, an identifier
	schemaWorkspaceMember   = "WorkspaceMember"
	schemaWorkspace         = "Workspace" //i18n:ignore OpenAPI schema name, an identifier
	schemaTokenResponse     = "TokenResponse"
	schemaHealth            = "Health" //i18n:ignore OpenAPI schema name, an identifier
	schemaOptimizerWorkers  = "TokenOptimizerWorkers"
	schemaOptimizerResolved = "TokenOptimizerResolved"
	schemaWorkspaceSettings = "WorkspaceSettings"
	schemaWorkspaceRunLimit = "WorkspaceRunLimit"
)

type openAPILocale string

func (l openAPILocale) msg(key string, args ...any) string {
	return i18n.T(string(l), key, args...)
}

func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	spec := buildOpenAPISpec(openAPILocale(i18n.RequestLocale(r)))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=3600") //i18n:ignore HTTP header value
	w.Header().Set("Vary", "Accept-Language, Cookie")       //i18n:ignore HTTP header value
	json.NewEncoder(w).Encode(spec)
}

func buildOpenAPISpec(l openAPILocale) openAPISpec { //nolint:funlen,maintidx // single declarative literal enumerating every API path, schema and response — long by nature, not branchy
	bearerAuth := map[string][]string{"BearerAuth": {}}
	apiKeyAuth := map[string][]string{"ApiKeyAuth": {}}
	security := []map[string][]string{bearerAuth, apiKeyAuth}

	tagHealth := l.msg("api.openapi.tags.health")
	tagAuth := l.msg("api.openapi.tags.auth")
	tagTasks := l.msg("api.openapi.tags.tasks")
	tagProjects := l.msg("api.openapi.tags.projects")
	tagWebhooks := l.msg("api.openapi.tags.webhooks")
	tagAutomations := l.msg("api.openapi.tags.automations")
	tagAudit := l.msg("api.openapi.tags.audit")
	tagAgents := l.msg("api.openapi.tags.agents")
	tagUsage := l.msg("api.openapi.tags.usage")
	tagChats := l.msg("api.openapi.tags.chats")
	tagEvents := l.msg("api.openapi.tags.events")

	idParam := openAPIParameter{Name: "id", In: "path", Required: true, Schema: openAPISchema{Type: "string"}}
	attachmentIDParam := openAPIParameter{Name: "attachmentId", In: "path", Required: true, Schema: openAPISchema{Type: "string"}}
	caseParam := openAPIParameter{Name: "case", In: "path", Required: true, Description: l.msg("api.openapi.params.case"), Schema: openAPISchema{Type: "string"}}
	qidParam := openAPIParameter{Name: "qid", In: "path", Required: true, Description: l.msg("api.openapi.params.qid"), Schema: openAPISchema{Type: "string"}}
	aidParam := openAPIParameter{Name: "aid", In: "path", Required: true, Description: l.msg("api.openapi.params.aid"), Schema: openAPISchema{Type: "string"}}
	runIDParam := openAPIParameter{Name: "runId", In: "path", Required: true, Description: l.msg("api.openapi.params.run_id"), Schema: openAPISchema{Type: "string"}}
	deliveryIDParam := openAPIParameter{Name: "deliveryId", In: "path", Required: true, Description: l.msg("api.openapi.params.delivery_id"), Schema: openAPISchema{Type: "string"}}
	jsonContent := func(ref string) map[string]openAPIContent {
		return map[string]openAPIContent{"application/json": {Schema: openAPISchema{Ref: "#/components/schemas/" + ref}}}
	}
	jsonBody := func(ref string) *openAPIRequestBody {
		return &openAPIRequestBody{Required: true, Content: jsonContent(ref)}
	}
	ok200 := func(ref string) map[string]openAPIResponse {
		return map[string]openAPIResponse{"200": {Description: http.StatusText(http.StatusOK), Content: jsonContent(ref)}}
	}
	ok200List := func(ref string) map[string]openAPIResponse {
		list := openAPISchema{Type: "array", Items: &openAPISchema{Ref: "#/components/schemas/" + ref}}
		return map[string]openAPIResponse{"200": {Description: http.StatusText(http.StatusOK), Content: map[string]openAPIContent{"application/json": {Schema: list}}}}
	}
	created := l.msg("api.openapi.responses.created")
	created201 := func(ref string) map[string]openAPIResponse {
		return map[string]openAPIResponse{"201": {Description: created, Content: jsonContent(ref)}}
	}
	noContent := map[string]openAPIResponse{"204": {Description: l.msg("api.openapi.responses.no_content")}}
	topicList := l.msg("api.openapi.responses.topic_list")
	unknownStream := l.msg("api.openapi.responses.unknown_stream")

	return openAPISpec{
		OpenAPI: "3.1.0",
		Info: openAPIInfo{
			Title:       l.msg("api.openapi.info.title"),
			Description: l.msg("api.openapi.info.description"),
			Version:     "1.0.0",
		},
		Servers: []openAPIServer{
			{URL: "http://localhost:8585", Description: l.msg("api.openapi.servers.local")},
		},
		Components: openAPIComponents{
			SecuritySchemes: map[string]openAPISecurityScheme{
				"BearerAuth": {Type: "http", Scheme: "bearer", BearerFormat: "JWT"},
				"ApiKeyAuth": {Type: "apiKey", In: "header", Name: "X-API-Key"},
			},
			Schemas: map[string]openAPISchema{
				schemaTask: {Type: "object", Properties: map[string]openAPISchema{
					"id":               {Type: "string"},
					"title":            {Type: "string"},
					"body":             {Type: "string", Description: l.msg("api.openapi.schemas.task.body", "route", "GET /tasks/{id}")},
					"stage":            {Type: "string", Format: "stage"},
					"status":           {Type: "string", Format: "run-status"},
					"priority":         {Type: "string"},
					"project_ids":      {Type: "array", Items: &openAPISchema{Type: "string"}},
					"tags":             {Type: "array", Items: &openAPISchema{Type: "string"}},
					"branch_name":      {Type: "string"},
					"current_run_id":   {Type: "string"},
					"current_instance": {Type: "string"},
					"is_blocked":       {Type: "boolean"},
					"blocked_by_count": {Type: "integer"},
					"created_at":       {Type: "string", Format: "date-time"},
					"updated_at":       {Type: "string", Format: "date-time"},
				}},
				schemaProject: {Type: "object", Properties: map[string]openAPISchema{
					"id":             {Type: "string"},
					"name":           {Type: "string"},
					"path":           {Type: "string"},
					"repo_url":       {Type: "string"},
					"default_branch": {Type: "string"},
					"description":    {Type: "string"},
					"created_at":     {Type: "string", Format: "date-time"},
				}},
				schemaAgent: {Type: "object", Properties: map[string]openAPISchema{
					"id":         {Type: "string"},
					"task_id":    {Type: "string"},
					"stage":      {Type: "string"},
					"status":     {Type: "string"},
					"started_at": {Type: "string", Format: "date-time"},
				}},
				schemaComment: {Type: "object", Properties: map[string]openAPISchema{
					"id":         {Type: "string"},
					"task_id":    {Type: "string"},
					"body":       {Type: "string"},
					"author":     {Type: "string"},
					"source":     {Type: "string"},
					"stage":      {Type: "string"},
					"created_at": {Type: "string", Format: "date-time"},
				}},
				schemaAttachment: {Type: "object", Properties: map[string]openAPISchema{
					"id":         {Type: "string"},
					"task_id":    {Type: "string"},
					"comment_id": {Type: "string"},
					"author":     {Type: "string"},
					"filename":   {Type: "string"},
					"mime_type":  {Type: "string"},
					"size":       {Type: "integer"},
					"created_at": {Type: "string", Format: "date-time"},
				}},
				schemaChat: {Type: "object", Properties: map[string]openAPISchema{
					"id":                   {Type: "string"},
					"workspace_id":         {Type: "string"},
					"agent_type":           {Type: "string"},
					"task_id":              {Type: "string"},
					"title":                {Type: "string"},
					"created_by":           {Type: "string"},
					"archived":             {Type: "boolean"},
					"status":               {Type: "string"},
					"last_message_preview": {Type: "string"},
					"created_at":           {Type: "string", Format: "date-time"},
					"updated_at":           {Type: "string", Format: "date-time"},
				}},
				schemaChatMessage: {Type: "object", Properties: map[string]openAPISchema{
					"id":           {Type: "string"},
					"chat_id":      {Type: "string"},
					"role":         {Type: "string"},
					"author":       {Type: "string"},
					"text":         {Type: "string"},
					"duration_ms":  {Type: "integer"},
					"input_tokens": {Type: "integer"},
					"created_at":   {Type: "string", Format: "date-time"},
				}},
				schemaFlag: {Type: "object", Properties: map[string]openAPISchema{
					"id":         {Type: "string"},
					"task_id":    {Type: "string"},
					"kind":       {Type: "string"},
					"label":      {Type: "string"},
					"body":       {Type: "string"},
					"status":     {Type: "string"},
					"created_at": {Type: "string", Format: "date-time"},
				}},
				schemaTestCase: {Type: "object", Properties: map[string]openAPISchema{
					"id":          {Type: "string"},
					"task_id":     {Type: "string"},
					"key":         {Type: "string", Description: l.msg("api.openapi.schemas.test_case.key", "param", "{case}")},
					"title":       {Type: "string"},
					"description": {Type: "string"},
					"steps":       {Type: "string"},
					"expected":    {Type: "string"},
					"priority":    {Type: "string", Description: l.msg("api.openapi.schemas.test_case.priority")},
					"status":      {Type: "string", Description: l.msg("api.openapi.schemas.test_case.status")},
					"covered":     {Type: "boolean"},
					"position":    {Type: "integer"},
					"created_by":  {Type: "string"},
					"updated_by":  {Type: "string"},
					"created_at":  {Type: "string", Format: "date-time"},
					"updated_at":  {Type: "string", Format: "date-time"},
					"runs":        {Type: "array", Description: l.msg("api.openapi.schemas.test_case.runs"), Items: &openAPISchema{Ref: "#/components/schemas/" + schemaTestCaseRun}},
				}},
				schemaReport: {Type: "object", Properties: map[string]openAPISchema{
					"id":          {Type: "string"},
					"task_id":     {Type: "string"},
					"source":      {Type: "string", Description: l.msg("api.openapi.schemas.report.source")},
					"author":      {Type: "string"},
					"stage":       {Type: "string"},
					"instance_id": {Type: "string"},
					"kind":        {Type: "string", Description: l.msg("api.openapi.schemas.report.kind")},
					"title":       {Type: "string"},
					"body":        {Type: "string", Description: l.msg("api.openapi.schemas.report.body")},
					"created_at":  {Type: "string", Format: "date-time"},
				}},
				schemaTestCaseRun: {Type: "object", Properties: map[string]openAPISchema{
					"id":                     {Type: "string"},
					"test_case_id":           {Type: "string"},
					"task_id":                {Type: "string"},
					"verdict":                {Type: "string", Description: l.msg("api.openapi.schemas.test_case_run.verdict")},
					"observed":               {Type: "string"},
					"note":                   {Type: "string"},
					"video_attachment_id":    {Type: "string"},
					"take_attachment_id":     {Type: "string"},
					"report_id":              {Type: "string", Description: l.msg("api.openapi.schemas.test_case_run.report_id")},
					"comment_id":             {Type: "string", Description: l.msg("api.openapi.schemas.test_case_run.comment_id")},
					"capture_attachment_ids": {Type: "array", Items: &openAPISchema{Type: "string"}},
					"author":                 {Type: "string"},
					"ran_at":                 {Type: "string", Format: "date-time"},
				}},
				schemaWebhook: {Type: "object", Properties: map[string]openAPISchema{
					"id":             {Type: "string"},
					"url":            {Type: "string"},
					"events":         {Type: "array", Items: &openAPISchema{Type: "string"}},
					"active":         {Type: "boolean"},
					"created_at":     {Type: "string", Format: "date-time"},
					"secret":         {Type: "string", Description: l.msg("api.openapi.schemas.webhook.secret")},
					"legacy_signing": {Type: "boolean", Description: l.msg("api.openapi.schemas.webhook.legacy_signing")},
				}},
				schemaWebhookDelivery: {Type: "object", Properties: map[string]openAPISchema{
					"id":           {Type: "string"},
					"webhook_id":   {Type: "string"},
					"event":        {Type: "string"},
					"status_code":  {Type: "integer"},
					"delivered_at": {Type: "string", Format: "date-time"},
				}},
				schemaAutomation: {Type: "object", Properties: map[string]openAPISchema{
					"id":           {Type: "string"},
					"name":         {Type: "string"},
					"description":  {Type: "string"},
					"enabled":      {Type: "boolean"},
					"spec":         {Type: "string"},
					"trigger_kind": {Type: "string"},
					"cron":         {Type: "string"},
					"tz":           {Type: "string"},
					"next_run_at":  {Type: "string", Format: "date-time"},
					"last_run_at":  {Type: "string", Format: "date-time"},
					"last_status":  {Type: "string"},
				}},
				schemaAutomationRun: {Type: "object", Properties: map[string]openAPISchema{
					"id":            {Type: "string"},
					"automation_id": {Type: "string"},
					"status":        {Type: "string"},
					"trigger":       {Type: "string"},
					"reason":        {Type: "string"},
					"started_at":    {Type: "string", Format: "date-time"},
					"finished_at":   {Type: "string", Format: "date-time"},
					"error":         {Type: "string"},
					"agent_runs":    {Type: "integer"},
				}},
				schemaAuditEntry: {Type: "object", Properties: map[string]openAPISchema{
					"id":           {Type: "string"},
					"kind":         {Type: "string"},
					"actor_id":     {Type: "string"},
					"resource_id":  {Type: "string"},
					"summary":      {Type: "string"},
					"created_at":   {Type: "string", Format: "date-time"},
					"workspace_id": {Type: "string"},
					"actor_type":   {Type: "string"},
					"actor_label":  {Type: "string"},
					"prev_hash":    {Type: "string"},
					"row_hash":     {Type: "string"},
				}},
				schemaAuditChainReport: {Type: "object", Properties: map[string]openAPISchema{
					"ok":        {Type: "boolean"},
					"checked":   {Type: "integer", Description: l.msg("api.openapi.schemas.audit_chain_report.checked")},
					"legacy":    {Type: "integer", Description: l.msg("api.openapi.schemas.audit_chain_report.legacy")},
					"broken_at": {Type: "string", Description: l.msg("api.openapi.schemas.audit_chain_report.broken_at")},
					"reason":    {Type: "string"},
					"cause":     {Type: "string", Description: l.msg("api.openapi.schemas.audit_chain_report.cause")},
					"broken_created_at": {Type: "string", Format: "date-time",
						Description: l.msg("api.openapi.schemas.audit_chain_report.broken_created_at")},
					"head": {Type: "string", Description: l.msg("api.openapi.schemas.audit_chain_report.head")},
				}},
				schemaAuthConfig: {Type: "object", Properties: map[string]openAPISchema{
					"registration_open":  {Type: "boolean"},
					"jwt_configured":     {Type: "boolean"},
					"api_key_configured": {Type: "boolean"},
					"dev_mode":           {Type: "boolean"},
				}},
				schemaUser: {Type: "object", Properties: map[string]openAPISchema{
					"id":         {Type: "string"},
					"email":      {Type: "string"},
					"name":       {Type: "string"},
					"role":       {Type: "string"},
					"created_at": {Type: "string", Format: "date-time"},
				}},
				schemaWorkspaceMember: {Type: "object", Properties: map[string]openAPISchema{
					"user_id":      {Type: "string"},
					"workspace_id": {Type: "string"},
					"role":         {Type: "string"},
				}},
				schemaWorkspace: {Type: "object", Properties: map[string]openAPISchema{
					"id":   {Type: "string"},
					"name": {Type: "string"},
				}},
				schemaTokenResponse: {Type: "object", Properties: map[string]openAPISchema{
					"access_token":  {Type: "string"},
					"refresh_token": {Type: "string"},
					"expires_at":    {Type: "string", Format: "date-time"},
					"token_type":    {Type: "string"},
				}},
				schemaHealth: {Type: "object", Properties: map[string]openAPISchema{
					"status":    {Type: "string"},
					"timestamp": {Type: "string", Format: "date-time"},
					"uptime":    {Type: "string"},
				}},
				schemaOptimizerWorkers: {Type: "object", Properties: map[string]openAPISchema{
					"workers": {Type: "array", Items: &openAPISchema{Type: "object", Properties: map[string]openAPISchema{
						"engine":       {Type: "string", Description: l.msg("api.openapi.schemas.token_optimizer.engine")},
						"available":    {Type: "boolean", Description: l.msg("api.openapi.schemas.token_optimizer.available")},
						"via":          {Type: "string", Description: l.msg("api.openapi.schemas.token_optimizer.via")},
						"provider":     {Type: "string", Description: l.msg("api.openapi.schemas.token_optimizer.provider")},
						"profile_name": {Type: "string", Description: l.msg("api.openapi.schemas.token_optimizer.profile_name")},
						"reason":       {Type: "string", Description: l.msg("api.openapi.schemas.token_optimizer.worker_reason")},
					}}},
				}},
				schemaWorkspaceSettings: {Type: "object", Properties: map[string]openAPISchema{
					settings.AgentMaxRunMinutesKey: {Ref: "#/components/schemas/" + schemaWorkspaceRunLimit},
				}},
				schemaWorkspaceRunLimit: {Type: "object", Description: l.msg("api.openapi.schemas.workspace_run_limit.about"), Properties: map[string]openAPISchema{
					"value":     {Type: "integer", Description: l.msg("api.openapi.schemas.workspace_run_limit.value")},
					"default":   {Type: "integer", Description: l.msg("api.openapi.schemas.workspace_run_limit.default")},
					"ceiling":   {Type: "integer", Description: l.msg("api.openapi.schemas.workspace_run_limit.ceiling")},
					"effective": {Type: "integer", Description: l.msg("api.openapi.schemas.workspace_run_limit.effective")},
				}},
				schemaOptimizerResolved: {Type: "object", Properties: map[string]openAPISchema{
					"level":           {Type: "string", Description: l.msg("api.openapi.schemas.token_optimizer.level")},
					"threshold_bytes": {Type: "integer"},
					"reason":          {Type: "string", Description: l.msg("api.openapi.schemas.token_optimizer.reason")},
					"read_engine":     {Type: "string", Description: l.msg("api.openapi.schemas.token_optimizer.engine")},
					"read_via":        {Type: "string", Description: l.msg("api.openapi.schemas.token_optimizer.via")},
					"read_model":      {Type: "string", Description: l.msg("api.openapi.schemas.token_optimizer.model")},
				}},
			},
		},
		Paths: map[string]openAPIPath{
			"/health":       {"get": {Tags: []string{tagHealth}, Summary: l.msg("api.openapi.ops.health_full.summary"), OperationID: "healthFull", Responses: ok200(schemaHealth)}},
			"/health/live":  {"get": {Tags: []string{tagHealth}, Summary: l.msg("api.openapi.ops.health_live.summary"), OperationID: "healthLive", Responses: ok200(schemaHealth)}},
			"/health/ready": {"get": {Tags: []string{tagHealth}, Summary: l.msg("api.openapi.ops.health_ready.summary"), OperationID: "healthReady", Responses: ok200(schemaHealth)}},

			"/auth/config":  {"get": {Tags: []string{tagAuth}, Summary: l.msg("api.openapi.ops.auth_config.summary"), OperationID: "authConfig", Responses: ok200(schemaAuthConfig)}},
			"/auth/refresh": {"post": {Tags: []string{tagAuth}, Summary: l.msg("api.openapi.ops.auth_refresh.summary"), OperationID: "authRefresh", Responses: ok200(schemaTokenResponse)}},
			"/auth/logout":  {"post": {Tags: []string{tagAuth}, Summary: l.msg("api.openapi.ops.auth_logout.summary"), OperationID: "authLogout", Security: security, Responses: ok200(schemaUser)}},
			"/auth/me":      {"get": {Tags: []string{tagAuth}, Summary: l.msg("api.openapi.ops.auth_me.summary"), OperationID: "authMe", Security: security, Responses: ok200(schemaUser)}},
			"/auth/workspaces/{id}/members": {
				"get":  {Tags: []string{tagAuth}, Summary: l.msg("api.openapi.ops.list_members.summary"), OperationID: "listMembers", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaWorkspaceMember)},
				"post": {Tags: []string{tagAuth}, Summary: l.msg("api.openapi.ops.add_member.summary"), OperationID: "addMember", Security: security, Parameters: []openAPIParameter{idParam}, RequestBody: jsonBody(schemaWorkspaceMember), Responses: map[string]openAPIResponse{"201": {Description: created}}},
			},
			"/auth/workspaces/{id}/members/{userId}": {
				"patch":  {Tags: []string{tagAuth}, Summary: l.msg("api.openapi.ops.update_member_role.summary"), OperationID: "updateMemberRole", Security: security, Parameters: []openAPIParameter{idParam, {Name: "userId", In: "path", Required: true, Schema: openAPISchema{Type: "string"}}}, RequestBody: jsonBody(schemaWorkspaceMember), Responses: ok200(schemaWorkspaceMember)},
				"delete": {Tags: []string{tagAuth}, Summary: l.msg("api.openapi.ops.remove_member.summary"), OperationID: "removeMember", Security: security, Parameters: []openAPIParameter{idParam, {Name: "userId", In: "path", Required: true, Schema: openAPISchema{Type: "string"}}}, Responses: noContent},
			},
			"/auth/workspaces/{id}/invitations": {
				"get": {Tags: []string{tagAuth}, Summary: l.msg("api.openapi.ops.list_invitations.summary"), OperationID: "listInvitations", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaWorkspaceMember)},
			},
			"/auth/workspaces/{id}/invitations/{invitationId}": {
				"patch":  {Tags: []string{tagAuth}, Summary: l.msg("api.openapi.ops.update_invitation.summary"), OperationID: "updateInvitation", Security: security, Parameters: []openAPIParameter{idParam, {Name: "invitationId", In: "path", Required: true, Schema: openAPISchema{Type: "string"}}}, RequestBody: jsonBody(schemaWorkspaceMember), Responses: ok200(schemaWorkspaceMember)},
				"delete": {Tags: []string{tagAuth}, Summary: l.msg("api.openapi.ops.delete_invitation.summary"), OperationID: "deleteInvitation", Security: security, Parameters: []openAPIParameter{idParam, {Name: "invitationId", In: "path", Required: true, Schema: openAPISchema{Type: "string"}}}, Responses: noContent},
			},

			"/tasks": {
				"get": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.list_tasks.summary"), OperationID: "listTasks", Security: security,
					Parameters: []openAPIParameter{
						{Name: "board", In: "query", Description: l.msg("api.openapi.ops.list_tasks.params.board"), Schema: openAPISchema{Type: "string"}},
						{Name: "stage", In: "query", Description: l.msg("api.openapi.ops.list_tasks.params.stage"), Schema: openAPISchema{Type: "string"}},
						{Name: "limit", In: "query", Description: l.msg("api.openapi.ops.list_tasks.params.limit"), Schema: openAPISchema{Type: "integer"}},
						{Name: "cursor", In: "query", Description: l.msg("api.openapi.ops.list_tasks.params.cursor"), Schema: openAPISchema{Type: "string"}},
					},
					Responses: ok200(schemaTask),
				},
				"post": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.create_task.summary"), OperationID: "createTask", Security: security, RequestBody: jsonBody(schemaTask), Responses: created201(schemaTask)},
			},
			"/tasks/{id}": {
				"get":    {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.get_task.summary"), OperationID: "getTask", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaTask)},
				"patch":  {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.update_task.summary"), OperationID: "updateTask", Security: security, Parameters: []openAPIParameter{idParam}, RequestBody: jsonBody(schemaTask), Responses: ok200(schemaTask)},
				"delete": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.delete_task.summary"), OperationID: "deleteTask", Security: security, Parameters: []openAPIParameter{idParam}, Responses: noContent},
			},
			"/tasks/{id}/comments": {
				"post": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.add_comment.summary"), OperationID: "addComment", Security: security, Parameters: []openAPIParameter{idParam}, RequestBody: jsonBody(schemaComment), Responses: created201(schemaComment)},
			},
			"/tasks/{id}/attachments": {
				"get":  {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.list_attachments.summary"), OperationID: "listAttachments", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaAttachment)},
				"post": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.upload_attachment.summary"), Description: l.msg("api.openapi.ops.upload_attachment.description"), OperationID: "uploadAttachment", Security: security, Parameters: []openAPIParameter{idParam}, RequestBody: jsonBody(schemaAttachment), Responses: ok200(schemaAttachment)},
			},
			"/task-attachments/{attachmentId}": {
				"get":    {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.serve_attachment.summary"), OperationID: "serveAttachment", Security: security, Parameters: []openAPIParameter{attachmentIDParam}, Responses: map[string]openAPIResponse{"200": {Description: l.msg("api.openapi.ops.serve_attachment.responses.ok")}}},
				"delete": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.delete_attachment.summary"), OperationID: "deleteAttachment", Security: security, Parameters: []openAPIParameter{attachmentIDParam}, Responses: noContent},
			},
			"/tasks/{id}/sections": {
				"get": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.list_sections.summary"), OperationID: "listSections", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaTask)},
			},
			"/tasks/{id}/sections/{slug}": {
				"put": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.set_section.summary"), OperationID: "setSection", Security: security, Parameters: []openAPIParameter{idParam, {Name: "slug", In: "path", Required: true, Schema: openAPISchema{Type: "string"}}}, RequestBody: jsonBody(schemaTask), Responses: ok200(schemaTask)},
			},
			"/tasks/{id}/sections/{slug}/items/{index}": {
				"patch": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.check_section_item.summary"), OperationID: "checkSectionItem", Security: security, Parameters: []openAPIParameter{idParam, {Name: "slug", In: "path", Required: true, Schema: openAPISchema{Type: "string"}}, {Name: "index", In: "path", Required: true, Schema: openAPISchema{Type: "integer"}}}, RequestBody: jsonBody(schemaTask), Responses: ok200(schemaTask)},
			},
			"/tasks/{id}/flags": {
				"get":  {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.list_flags.summary"), OperationID: "listFlags", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaFlag)},
				"post": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.create_flag.summary"), OperationID: "createFlag", Security: security, Parameters: []openAPIParameter{idParam}, RequestBody: jsonBody(schemaFlag), Responses: created201(schemaFlag)},
			},
			"/tasks/{id}/pull-requests": {
				"get": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.list_pull_requests.summary"), Description: l.msg("api.openapi.ops.list_pull_requests.description"), OperationID: "listPullRequests", Security: security, Parameters: []openAPIParameter{idParam}, Responses: map[string]openAPIResponse{"200": {Description: l.msg("api.openapi.ops.list_pull_requests.responses.ok")}}},
			},
			"/tasks/{id}/reports": {
				"get":  {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.list_reports.summary"), OperationID: "listReports", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaReport)},
				"post": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.add_report.summary"), Description: l.msg("api.openapi.ops.add_report.description"), OperationID: "addReport", Security: security, Parameters: []openAPIParameter{idParam}, RequestBody: jsonBody(schemaReport), Responses: created201(schemaReport)},
			},
			"/tasks/{id}/test-cases": {
				"get":  {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.list_test_cases.summary"), OperationID: "listTestCases", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaTestCase)},
				"post": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.add_test_cases.summary"), Description: l.msg("api.openapi.ops.add_test_cases.description"), OperationID: "addTestCases", Security: security, Parameters: []openAPIParameter{idParam}, RequestBody: jsonBody(schemaTestCase), Responses: created201(schemaTestCase)},
			},
			"/tasks/{id}/test-cases/import-body": {
				"post": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.import_test_cases_from_body.summary"), OperationID: "importTestCasesFromBody", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaTestCase)},
			},
			"/tasks/{id}/test-cases/{case}": {
				"patch":  {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.update_test_case.summary"), OperationID: "updateTestCase", Security: security, Parameters: []openAPIParameter{idParam, caseParam}, RequestBody: jsonBody(schemaTestCase), Responses: ok200(schemaTestCase)},
				"delete": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.delete_test_case.summary"), OperationID: "deleteTestCase", Security: security, Parameters: []openAPIParameter{idParam, caseParam}, Responses: map[string]openAPIResponse{"200": {Description: l.msg("api.openapi.responses.deleted")}}},
			},
			"/tasks/{id}/test-cases/{case}/runs": {
				"post": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.record_test_case_run.summary"), Description: l.msg("api.openapi.ops.record_test_case_run.description"), OperationID: "recordTestCaseRun", Security: security, Parameters: []openAPIParameter{idParam, caseParam}, RequestBody: jsonBody(schemaTestCaseRun), Responses: map[string]openAPIResponse{"201": {Description: created}}},
			},
			"/test-cases": {
				"get": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.list_workspace_test_cases.summary"), Description: l.msg("api.openapi.ops.list_workspace_test_cases.description"), OperationID: "listWorkspaceTestCases", Security: security,
					Parameters: []openAPIParameter{
						{Name: "status", In: "query", Description: l.msg("api.openapi.ops.list_workspace_test_cases.params.status"), Schema: openAPISchema{Type: "string"}},
						{Name: "board_id", In: "query", Schema: openAPISchema{Type: "string"}},
						{Name: "project_id", In: "query", Schema: openAPISchema{Type: "string"}},
						{Name: "q", In: "query", Description: l.msg("api.openapi.ops.list_workspace_test_cases.params.q"), Schema: openAPISchema{Type: "string"}},
						{Name: "uncovered", In: "query", Description: l.msg("api.openapi.ops.list_workspace_test_cases.params.uncovered"), Schema: openAPISchema{Type: "string"}},
						{Name: "limit", In: "query", Description: l.msg("api.openapi.ops.list_workspace_test_cases.params.limit"), Schema: openAPISchema{Type: "integer"}},
					},
					Responses: map[string]openAPIResponse{"200": {Description: http.StatusText(http.StatusOK)}},
				},
			},
			"/tasks/{id}/questions": {
				"post": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.ask_question.summary"), Description: l.msg("api.openapi.ops.ask_question.description"), OperationID: "askQuestion", Security: security, Parameters: []openAPIParameter{idParam},
					Responses: map[string]openAPIResponse{"201": {Description: created}, "409": {Description: http.StatusText(http.StatusConflict)}, "429": {Description: http.StatusText(http.StatusTooManyRequests)}}},
			},
			"/tasks/{id}/questions/{qid}/answer": {
				"post": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.answer_question.summary"), Description: l.msg("api.openapi.ops.answer_question.description"), OperationID: "answerQuestion", Security: security, Parameters: []openAPIParameter{idParam, qidParam},
					Responses: map[string]openAPIResponse{"200": {Description: http.StatusText(http.StatusOK)}, "409": {Description: http.StatusText(http.StatusConflict)}}},
			},
			"/tasks/{id}/questions/{qid}/cancel": {
				"post": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.cancel_question.summary"), OperationID: "cancelQuestion", Security: security, Parameters: []openAPIParameter{idParam, qidParam},
					Responses: map[string]openAPIResponse{"200": {Description: http.StatusText(http.StatusOK)}, "409": {Description: http.StatusText(http.StatusConflict)}}},
			},
			"/tasks/{id}/approvals": {
				"get": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.list_approvals.summary"), OperationID: "listApprovals", Security: security, Parameters: []openAPIParameter{idParam},
					Responses: map[string]openAPIResponse{"200": {Description: http.StatusText(http.StatusOK)}}},
				"post": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.request_approval.summary"), Description: l.msg("api.openapi.ops.request_approval.description"), OperationID: "requestApproval", Security: security, Parameters: []openAPIParameter{idParam},
					Responses: map[string]openAPIResponse{"201": {Description: created}}},
			},
			"/tasks/{id}/approvals/{aid}/decide": {
				"post": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.decide_approval.summary"), Description: l.msg("api.openapi.ops.decide_approval.description"), OperationID: "decideApproval", Security: security, Parameters: []openAPIParameter{idParam, aidParam},
					Responses: map[string]openAPIResponse{"200": {Description: http.StatusText(http.StatusOK)}, "400": {Description: http.StatusText(http.StatusBadRequest)}}},
			},
			"/tasks/{id}/approvals/{aid}": {
				"delete": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.delete_approval.summary"), OperationID: "deleteApproval", Security: security, Parameters: []openAPIParameter{idParam, aidParam},
					Responses: map[string]openAPIResponse{"200": {Description: http.StatusText(http.StatusOK)}, "409": {Description: http.StatusText(http.StatusConflict)}}},
			},
			"/inbox": {
				"get": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.inbox.summary"), Description: l.msg("api.openapi.ops.inbox.description"), OperationID: "inbox", Security: security,
					Responses: map[string]openAPIResponse{"200": {Description: http.StatusText(http.StatusOK)}}},
			},
			"/tasks/{id}/relaunch": {
				"post": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.relaunch_task.summary"), OperationID: "relaunchTask", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaTask)},
			},
			"/tasks/{id}/merge": {
				"post": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.merge_task.summary"), Description: l.msg("api.openapi.ops.merge_task.description"), OperationID: "mergeTask", Security: security, Parameters: []openAPIParameter{idParam},
					Responses: map[string]openAPIResponse{"200": {Description: http.StatusText(http.StatusOK)}, "400": {Description: http.StatusText(http.StatusBadRequest)}, "409": {Description: http.StatusText(http.StatusConflict)}}},
			},
			"/tasks/{id}/clone": {
				"post": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.clone_task.summary"), OperationID: "cloneTask", Security: security, Parameters: []openAPIParameter{idParam}, Responses: created201(schemaTask)},
			},
			"/tasks/{id}/dependencies": {
				"get":  {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.list_deps.summary"), OperationID: "listDeps", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaTask)},
				"post": {Tags: []string{tagTasks}, Summary: l.msg("api.openapi.ops.add_dep.summary"), OperationID: "addDep", Security: security, Parameters: []openAPIParameter{idParam}, Responses: map[string]openAPIResponse{"201": {Description: created}}},
			},

			"/projects": {
				"get":  {Tags: []string{tagProjects}, Summary: l.msg("api.openapi.ops.list_projects.summary"), OperationID: "listProjects", Security: security, Responses: ok200(schemaProject)},
				"post": {Tags: []string{tagProjects}, Summary: l.msg("api.openapi.ops.create_project.summary"), OperationID: "createProject", Security: security, RequestBody: jsonBody(schemaProject), Responses: created201(schemaProject)},
			},
			"/projects/{id}": {
				"get":    {Tags: []string{tagProjects}, Summary: l.msg("api.openapi.ops.get_project.summary"), OperationID: "getProject", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaProject)},
				"patch":  {Tags: []string{tagProjects}, Summary: l.msg("api.openapi.ops.update_project.summary"), OperationID: "updateProject", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaProject)},
				"delete": {Tags: []string{tagProjects}, Summary: l.msg("api.openapi.ops.delete_project.summary"), OperationID: "deleteProject", Security: security, Parameters: []openAPIParameter{idParam}, Responses: noContent},
			},
			"/projects/upload": {
				"post": {Tags: []string{tagProjects}, Summary: l.msg("api.openapi.ops.upload_project.summary"), Description: l.msg("api.openapi.ops.upload_project.description"), OperationID: "uploadProject", Security: security,
					Responses: map[string]openAPIResponse{"201": {Description: created}, "400": {Description: http.StatusText(http.StatusBadRequest)}, "413": {Description: http.StatusText(http.StatusRequestEntityTooLarge)}}},
			},
			"/projects/{id}/upload": {
				"post": {Tags: []string{tagProjects}, Summary: l.msg("api.openapi.ops.reupload_project.summary"), Description: l.msg("api.openapi.ops.reupload_project.description"), OperationID: "reuploadProject", Security: security,
					Parameters: []openAPIParameter{idParam,
						{Name: "mode", In: "query", Description: l.msg("api.openapi.ops.reupload_project.params.mode"), Schema: openAPISchema{Type: "string"}},
					},
					Responses: map[string]openAPIResponse{"200": {Description: http.StatusText(http.StatusOK)}, "400": {Description: http.StatusText(http.StatusBadRequest)}, "409": {Description: http.StatusText(http.StatusConflict)}, "413": {Description: http.StatusText(http.StatusRequestEntityTooLarge)}}},
			},
			"/projects/{id}/branches": {
				"get": {Tags: []string{tagProjects}, Summary: l.msg("api.openapi.ops.project_branches.summary"), OperationID: "projectBranches", Security: security, Parameters: []openAPIParameter{idParam},
					Responses: map[string]openAPIResponse{"200": {Description: http.StatusText(http.StatusOK)}}},
			},
			"/projects/{id}/archive": {
				"get": {Tags: []string{tagProjects}, Summary: l.msg("api.openapi.ops.project_archive.summary"), OperationID: "projectArchive", Security: security,
					Parameters: []openAPIParameter{idParam,
						{Name: "branch", In: "query", Description: l.msg("api.openapi.ops.project_archive.params.branch"), Schema: openAPISchema{Type: "string"}},
					},
					Responses: map[string]openAPIResponse{"200": {Description: http.StatusText(http.StatusOK)}, "404": {Description: http.StatusText(http.StatusNotFound)}}},
			},

			"/webhooks": {
				"get":  {Tags: []string{tagWebhooks}, Summary: l.msg("api.openapi.ops.list_webhooks.summary"), OperationID: "listWebhooks", Security: security, Responses: ok200(schemaWebhook)},
				"post": {Tags: []string{tagWebhooks}, Summary: l.msg("api.openapi.ops.create_webhook.summary"), OperationID: "createWebhook", Security: security, RequestBody: jsonBody(schemaWebhook), Responses: map[string]openAPIResponse{"201": {Description: l.msg("api.openapi.ops.create_webhook.responses.created"), Content: jsonContent(schemaWebhook)}}},
			},
			"/webhooks/events": {
				"get": {Tags: []string{tagWebhooks}, Summary: l.msg("api.openapi.ops.list_webhook_events.summary"), OperationID: "listWebhookEvents", Security: security, Responses: map[string]openAPIResponse{"200": {Description: l.msg("api.openapi.ops.list_webhook_events.responses.ok")}}},
			},
			"/webhooks/{id}": {
				"delete": {Tags: []string{tagWebhooks}, Summary: l.msg("api.openapi.ops.delete_webhook.summary"), OperationID: "deleteWebhook", Security: security, Parameters: []openAPIParameter{idParam}, Responses: noContent},
			},
			"/webhooks/{id}/rotate-secret": {
				"post": {Tags: []string{tagWebhooks}, Summary: l.msg("api.openapi.ops.rotate_webhook_secret.summary"), OperationID: "rotateWebhookSecret", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaWebhook)},
			},
			"/webhooks/{id}/test": {
				"post": {Tags: []string{tagWebhooks}, Summary: l.msg("api.openapi.ops.test_webhook.summary"), OperationID: "testWebhook", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaWebhookDelivery)},
			},
			"/webhooks/{id}/deliveries": {
				"get": {Tags: []string{tagWebhooks}, Summary: l.msg("api.openapi.ops.list_webhook_deliveries.summary"), OperationID: "listWebhookDeliveries", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200List(schemaWebhookDelivery)},
			},
			"/webhooks/{id}/deliveries/{deliveryId}/retry": {
				"post": {Tags: []string{tagWebhooks}, Summary: l.msg("api.openapi.ops.retry_webhook_delivery.summary"), OperationID: "retryWebhookDelivery", Security: security, Parameters: []openAPIParameter{idParam, deliveryIDParam}, Responses: map[string]openAPIResponse{"202": {Description: http.StatusText(http.StatusAccepted)}, "404": {Description: http.StatusText(http.StatusNotFound)}}},
			},

			"/automations": {
				"get":  {Tags: []string{tagAutomations}, Summary: l.msg("api.openapi.ops.list_automations.summary"), OperationID: "listAutomations", Security: security, Responses: ok200(schemaAutomation)},
				"post": {Tags: []string{tagAutomations}, Summary: l.msg("api.openapi.ops.create_automation.summary"), OperationID: "createAutomation", Security: security, RequestBody: jsonBody(schemaAutomation), Responses: created201(schemaAutomation)},
			},
			"/automations/validate": {
				"post": {Tags: []string{tagAutomations}, Summary: l.msg("api.openapi.ops.validate_automation.summary"), OperationID: "validateAutomation", Security: security, RequestBody: jsonBody(schemaAutomation), Responses: ok200(schemaAutomation)},
			},
			"/automations/{id}": {
				"get":    {Tags: []string{tagAutomations}, Summary: l.msg("api.openapi.ops.get_automation.summary"), OperationID: "getAutomation", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaAutomation)},
				"put":    {Tags: []string{tagAutomations}, Summary: l.msg("api.openapi.ops.update_automation.summary"), OperationID: "updateAutomation", Security: security, Parameters: []openAPIParameter{idParam}, RequestBody: jsonBody(schemaAutomation), Responses: ok200(schemaAutomation)},
				"delete": {Tags: []string{tagAutomations}, Summary: l.msg("api.openapi.ops.delete_automation.summary"), OperationID: "deleteAutomation", Security: security, Parameters: []openAPIParameter{idParam}, Responses: noContent},
			},
			"/automations/{id}/enabled": {
				"put": {Tags: []string{tagAutomations}, Summary: l.msg("api.openapi.ops.set_automation_enabled.summary"), OperationID: "setAutomationEnabled", Security: security, Parameters: []openAPIParameter{idParam}, RequestBody: jsonBody(schemaAutomation), Responses: ok200(schemaAutomation)},
			},
			"/automations/{id}/run": {
				"post": {Tags: []string{tagAutomations}, Summary: l.msg("api.openapi.ops.run_automation.summary"), OperationID: "runAutomation", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaAutomationRun)},
			},
			"/automations/{id}/runs": {
				"get": {Tags: []string{tagAutomations}, Summary: l.msg("api.openapi.ops.list_automation_runs.summary"), OperationID: "listAutomationRuns", Security: security, Parameters: []openAPIParameter{idParam, {Name: "limit", In: "query", Schema: openAPISchema{Type: "integer"}}}, Responses: ok200(schemaAutomationRun)},
			},
			"/automations/{id}/runs/{runId}": {
				"get": {Tags: []string{tagAutomations}, Summary: l.msg("api.openapi.ops.get_automation_run.summary"), OperationID: "getAutomationRun", Security: security, Parameters: []openAPIParameter{idParam, runIDParam}, Responses: ok200(schemaAutomationRun)},
			},
			"/automations/{id}/runs/{runId}/cancel": {
				"post": {Tags: []string{tagAutomations}, Summary: l.msg("api.openapi.ops.cancel_automation_run.summary"), OperationID: "cancelAutomationRun", Security: security, Parameters: []openAPIParameter{idParam, runIDParam}, Responses: noContent},
			},

			"/audit/verify": {
				"get": {Tags: []string{tagAudit}, Summary: l.msg("api.openapi.ops.verify_audit.summary"), OperationID: "verifyAudit", Security: security, Responses: ok200(schemaAuditChainReport)},
			},
			"/audit": {
				"get": {Tags: []string{tagAudit}, Summary: l.msg("api.openapi.ops.list_audit.summary"), OperationID: "listAudit", Security: security,
					Parameters: []openAPIParameter{
						{Name: "kind", In: "query", Schema: openAPISchema{Type: "string"}},
						{Name: "actor_id", In: "query", Schema: openAPISchema{Type: "string"}},
						{Name: "resource_id", In: "query", Schema: openAPISchema{Type: "string"}},
						{Name: "since", In: "query", Schema: openAPISchema{Type: "string", Format: "date-time"}},
						{Name: "until", In: "query", Schema: openAPISchema{Type: "string", Format: "date-time"}},
						{Name: "limit", In: "query", Schema: openAPISchema{Type: "integer"}},
						{Name: "offset", In: "query", Schema: openAPISchema{Type: "integer"}},
					},
					Responses: ok200(schemaAuditEntry),
				},
			},

			"/agents":           {"get": {Tags: []string{tagAgents}, Summary: l.msg("api.openapi.ops.list_agents.summary"), OperationID: "listAgents", Security: security, Responses: ok200(schemaAgent)}},
			"/agents/{id}":      {"get": {Tags: []string{tagAgents}, Summary: l.msg("api.openapi.ops.get_agent.summary"), OperationID: "getAgent", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaAgent)}},
			"/agents/{id}/stop": {"post": {Tags: []string{tagAgents}, Summary: l.msg("api.openapi.ops.stop_agent.summary"), OperationID: "stopAgent", Security: security, Parameters: []openAPIParameter{idParam}, Responses: noContent}},
			"/agents/{id}/logs": {"get": {Tags: []string{tagAgents}, Summary: l.msg("api.openapi.ops.agent_logs.summary"), OperationID: "agentLogs", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaAgent)}},

			"/workspace-settings": {
				"get": {Tags: []string{tagAgents}, Summary: l.msg("api.openapi.ops.get_workspace_settings.summary"), OperationID: "getWorkspaceSettings", Security: security, Responses: ok200(schemaWorkspaceSettings)},
			},
			"/workspace-settings/{key}": {
				"put": {Tags: []string{tagAgents}, Summary: l.msg("api.openapi.ops.put_workspace_setting.summary"), OperationID: "putWorkspaceSetting", Security: security,
					Parameters: []openAPIParameter{{Name: "key", In: "path", Required: true, Description: l.msg("api.openapi.params.setting_key"), Schema: openAPISchema{Type: "string"}}},
					RequestBody: &openAPIRequestBody{Required: true, Content: map[string]openAPIContent{
						"application/json": {Schema: openAPISchema{Type: "integer", Description: l.msg("api.openapi.schemas.workspace_run_limit.body")}},
					}},
					Responses: ok200(schemaWorkspaceSettings)},
			},

			"/usage":          {"get": {Tags: []string{tagUsage}, Summary: l.msg("api.openapi.ops.get_usage.summary"), OperationID: "getUsage", Security: security, Responses: ok200(schemaAgent)}},
			"/usage/sessions": {"get": {Tags: []string{tagUsage}, Summary: l.msg("api.openapi.ops.list_sessions.summary"), OperationID: "listSessions", Security: security, Responses: ok200(schemaAgent)}},
			"/token-optimizer/workers": {
				"get": {Tags: []string{tagUsage}, Summary: l.msg("api.openapi.ops.list_token_optimizer_workers.summary"),
					Description: l.msg("api.openapi.ops.list_token_optimizer_workers.description"),
					OperationID: "listTokenOptimizerWorkers", Security: security, Responses: ok200(schemaOptimizerWorkers)},
			},
			"/token-optimizer/resolve": {
				"get": {Tags: []string{tagUsage}, Summary: l.msg("api.openapi.ops.resolve_token_optimizer.summary"), OperationID: "resolveTokenOptimizer", Security: security,
					Parameters: []openAPIParameter{
						{Name: "agent", In: "query", Schema: openAPISchema{Type: "string"}},
					},
					Responses: ok200(schemaOptimizerResolved)},
			},

			"/chats": {
				"get":  {Tags: []string{tagChats}, Summary: l.msg("api.openapi.ops.list_chats.summary"), OperationID: "listChats", Security: security, Parameters: []openAPIParameter{{Name: "include_archived", In: "query", Schema: openAPISchema{Type: "boolean"}}}, Responses: ok200(schemaChat)},
				"post": {Tags: []string{tagChats}, Summary: l.msg("api.openapi.ops.create_chat.summary"), OperationID: "createChat", Security: security, RequestBody: jsonBody(schemaChat), Responses: created201(schemaChat)},
			},
			"/chats/{id}": {
				"get":    {Tags: []string{tagChats}, Summary: l.msg("api.openapi.ops.get_chat.summary"), OperationID: "getChat", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaChat)},
				"patch":  {Tags: []string{tagChats}, Summary: l.msg("api.openapi.ops.update_chat.summary"), OperationID: "updateChat", Security: security, Parameters: []openAPIParameter{idParam}, RequestBody: jsonBody(schemaChat), Responses: ok200(schemaChat)},
				"delete": {Tags: []string{tagChats}, Summary: l.msg("api.openapi.ops.delete_chat.summary"), OperationID: "deleteChat", Security: security, Parameters: []openAPIParameter{idParam}, Responses: noContent},
			},
			"/chats/{id}/reset": {
				"post": {Tags: []string{tagChats}, Summary: l.msg("api.openapi.ops.reset_chat.summary"), OperationID: "resetChat", Security: security, Parameters: []openAPIParameter{idParam}, Responses: ok200(schemaChat)},
			},
			"/chats/{id}/messages": {
				"get": {Tags: []string{tagChats}, Summary: l.msg("api.openapi.ops.list_chat_messages.summary"), OperationID: "listChatMessages", Security: security,
					Parameters: []openAPIParameter{idParam,
						{Name: "limit", In: "query", Schema: openAPISchema{Type: "integer"}},
						{Name: "before", In: "query", Description: l.msg("api.openapi.ops.list_chat_messages.params.before"), Schema: openAPISchema{Type: "string"}},
					},
					Responses: ok200(schemaChatMessage),
				},
				"post": {Tags: []string{tagChats}, Summary: l.msg("api.openapi.ops.post_chat_message.summary"), OperationID: "postChatMessage", Security: security, Parameters: []openAPIParameter{idParam}, RequestBody: jsonBody(schemaChatMessage), Responses: map[string]openAPIResponse{"202": {Description: l.msg("api.openapi.ops.post_chat_message.responses.accepted"), Content: jsonContent(schemaChatMessage)}, "409": {Description: l.msg("api.openapi.ops.post_chat_message.responses.conflict")}}},
			},
			"/chats/events": {
				"get": {Tags: []string{tagChats}, Summary: l.msg("api.openapi.ops.chat_events.summary"), OperationID: "chatEvents", Security: security, Responses: ok200(schemaChatMessage)},
			},
			"/events": {
				"get": {Tags: []string{tagEvents}, Summary: l.msg("api.openapi.ops.open_event_stream.summary"), OperationID: "openEventStream", Security: security,
					Parameters: []openAPIParameter{
						{Name: "topics", In: "query", Description: l.msg("api.openapi.ops.open_event_stream.params.topics"), Schema: openAPISchema{Type: "string"}},
					},
					Responses: map[string]openAPIResponse{"200": {Description: l.msg("api.openapi.ops.open_event_stream.responses.ok")}},
				},
			},
			"/events/{id}/subscribe": {
				"post": {Tags: []string{tagEvents}, Summary: l.msg("api.openapi.ops.subscribe_topics.summary"), OperationID: "subscribeTopics", Security: security, Parameters: []openAPIParameter{idParam}, Responses: map[string]openAPIResponse{"200": {Description: topicList}, "404": {Description: unknownStream}}},
			},
			"/events/{id}/unsubscribe": {
				"post": {Tags: []string{tagEvents}, Summary: l.msg("api.openapi.ops.unsubscribe_topics.summary"), OperationID: "unsubscribeTopics", Security: security, Parameters: []openAPIParameter{idParam}, Responses: map[string]openAPIResponse{"200": {Description: topicList}, "404": {Description: unknownStream}}},
			},
		},
	}
}
