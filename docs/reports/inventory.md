# IDE / CLI built-in tool inventory

Scan date: **2026-09-11**. Mục tiêu: bảng inventory built-in tools theo IDE/CLI (không phải sample wire-format Bash/Read/Edit).

| Product | Version scanned | Primary source |
| --- | --- | --- |
| Claude Code | `2.1.268` | Official docs + local binary strings |
| Cursor Agent | CLI `2026.06.24-00-45-58-9f61de7` · App `3.17.19` | `cursor-agent` bundles + `cursor-agent-exec` |
| Codex CLI | local cask **broken** (`0.98.0` missing binary); inventory from `openai/codex` **main** + KB v0.136 | GitHub handlers/specs |
| Antigravity | App `2.2.1` · CLI `agy 1.1.10` | `agy` / `language_server` `CORTEX_STEP_TYPE_*` |
| Gemini CLI | `0.49.0` | Official `docs/reference/tools.md` + npm package |

**Notes chung**

- MCP / plugins / skills: dynamic — không liệt kê hết; pattern ghi ở mỗi IDE.
- Tool surface phụ thuộc model, feature flag, plan, OS, sandbox.
- Local Codex binary symlink gãy → không verify strings trên máy này.

---

## 1. Claude Code

**Sources:** [code.claude.com/docs/en/tools](https://code.claude.com/docs/en/tools) · binary `~/.local/share/claude/versions/2.1.268` (45/45 tên docs có trong binary).

### Built-in tools

| Tool | Category | Notes |
| --- | --- | --- |
| `Agent` | orchestration | Subagent / teammate khi agent teams bật |
| `Artifact` | publish | HTML/Markdown artifact trên claude.ai (plan) |
| `AskUserQuestion` | interaction | Multi-choice clarification |
| `Bash` | execute | Shell (permission gated) |
| `CronCreate` | schedule | Session-scoped cron |
| `CronDelete` | schedule | |
| `CronList` | schedule | |
| `Edit` | edit | Targeted file edit |
| `EndConversation` | session | ≥ v2.1.213 |
| `EnterPlanMode` | plan | |
| `EnterWorktree` | git | Isolated worktree |
| `ExitPlanMode` | plan | |
| `ExitWorktree` | git | |
| `Glob` | search | |
| `Grep` | search | |
| `ListAgents` | orchestration | Cross-session list (≥ v2.1.224) |
| `ListMcpResourcesTool` | mcp | |
| `LSP` | code intel | Definitions / refs / diagnostics |
| `Monitor` | execute | Background cmd / WebSocket feed |
| `NotebookEdit` | edit | Jupyter |
| `PowerShell` | execute | Native PowerShell (availability) |
| `PushNotification` | notify | Desktop / Remote Control |
| `Read` | read | |
| `ReadMcpResourceTool` | mcp | |
| `RemoteTrigger` | schedule | Routines on claude.ai |
| `ReportFindings` | review | Structured code-review findings |
| `ScheduleWakeup` | schedule | `/loop` wakeup |
| `SendFeedback` | product | Draft feedback (≥ v2.1.238) |
| `SendMessage` | orchestration | Teammate / other sessions |
| `SendUserFile` | deliver | Remote Control / cloud |
| `ShareOnboardingGuide` | team | `ONBOARDING.md` share |
| `Skill` | skills | |
| `TaskCreate` | tasks | Task list (model-gated) |
| `TaskGet` | tasks | |
| `TaskList` | tasks | |
| `TaskOutput` | tasks | Deprecated → prefer `Read` on output path |
| `TaskStop` | tasks | |
| `TaskUpdate` | tasks | |
| `TodoWrite` | tasks | Disabled by default khi Task* bật |
| `ToolSearch` | meta | Deferred / MCP tool search |
| `WaitForMcpServers` | mcp | Khi ToolSearch tắt |
| `WebFetch` | web | |
| `WebSearch` | web | |
| `Workflow` | orchestration | Dynamic multi-subagent script |
| `Write` | edit | Create / overwrite |

**Count:** 45 built-ins (docs + binary).

**Dynamic:** `mcp__<server>__<tool>` từ MCP servers.

---

## 2. Cursor (Agent CLI + Agent Exec)

**Sources:** permissions docs (`Shell`/`Read`/`Write`/`WebFetch`/`Mcp`) · `~/.local/share/cursor-agent/.../4172.index.js` · `/Applications/Cursor.app/.../cursor-agent-exec/dist/main.js`.

### Built-in tools (agent-facing)

| Tool | Category | Evidence |
| --- | --- | --- |
| `Shell` | execute | CLI permissions + agent-exec |
| `Read` | read | |
| `Write` | edit | |
| `Delete` | edit | agent-exec |
| `StrReplace` | edit | agent-exec (thấp tần suất) |
| `ApplyPatch` | edit | agent-exec |
| `Grep` | search | permissions gap vs Read documented |
| `Glob` | search | |
| `SemanticSearch` | search | agent-exec |
| `CodeLineage` | search | agent-exec / CLI bundle |
| `WebFetch` | web | permissions |
| `WebSearch` | web | |
| `AskQuestion` | interaction | |
| `GenerateImage` | media | |
| `SwitchMode` | plan/mode | |
| `CreatePlan` | plan | agent-exec |
| `Task` | orchestration | |
| `TodoWrite` | tasks | |
| `Await` / `AwaitShell` | sync | agent-exec |
| `EditNotebook` | edit | |
| `ReadLints` | code intel | |
| `ListMcpResources` | mcp | |
| `FetchMcpResource` | mcp | |
| `CallMcpTool` | mcp | invoke MCP tool |
| `ComputerUse` | browser/OS | string present; availability gated |

**Permission vocabulary (config):** `Shell(cmd)`, `Read(path)`, `Write(path)`, `WebFetch(domain)`, `Mcp(server:tool)`.

**Dynamic:** MCP via `mcp.json` / `/mcp`.

---

## 3. Codex CLI

**Sources:** `openai/codex` main (`codex-rs/core/src/tools/handlers/*`, `spec_plan.rs`) · [KB built-in surface v0.136](https://codex.danielvaughan.com/2026/06/03/codex-cli-built-in-tool-surface-complete-reference-shell-file-search-image-multi-agent/).

Local Homebrew cask `codex 0.98.0` **không có binary** trên máy scan → không verify strings local.

### Core / always-relevant built-ins (current main)

| Tool | Category | Notes |
| --- | --- | --- |
| `shell_command` | execute | Primary shell (config-dependent) |
| `shell` / `local_shell` / `container.exec` | execute | Aliases / env modes |
| `exec_command` | execute | Unified exec (PTY) |
| `write_stdin` | execute | Feed PTY session |
| `apply_patch` | edit | V4A freeform/JSON patch |
| `update_plan` | plan | Structured step checklist |
| `view_image` | media | Attach local image |
| `image_gen` / `imagegen` | media | Namespaced image gen |
| `web_search` / `web.run` | web | Hosted / cached / live |
| `tool_search` | meta | BM25 over tool catalogue |
| `list_mcp_resources` | mcp | |
| `list_mcp_resource_templates` | mcp | |
| `read_mcp_resource` | mcp | |
| `request_user_input` | interaction | (+ `request_user_input_async`) |
| `request_permissions` | policy | |
| `send_message_to_user_async` | interaction | |
| `get_context_remaining` | meta | |
| `new_context` | meta | Context window ops |
| `wait_for_environment` | env | |
| `list_available_plugins_to_install` | plugins | |
| `request_plugin_install` | plugins | |
| `clock.sleep` | util | Namespaced `clock` / `sleep` |
| `clock.curr_time` | util | |
| code-mode public + wait tools | code mode | `PUBLIC_TOOL_NAME` / `WAIT_TOOL_NAME` |

### Multi-agent

| Tool | Notes |
| --- | --- |
| `spawn_agent` | v1/v2 |
| `send_input` | |
| `wait_agent` | |
| `close_agent` | |
| `resume_agent` | |
| `send_message` | v2 |
| `followup_task` | v2 |
| `interrupt_agent` | v2 |
| `list_agents` | v2 |
| `spawn_agents_on_csv` | KB batch mode (feature) |

### KB v0.136 dedicated FS tools (may be shell-prefer / feature-gated on newer builds)

| Tool | Category |
| --- | --- |
| `read_file` | read |
| `list_dir` | read |
| `glob_file_search` | search |
| `rg` | search |
| `git` | vcs |

**Dynamic:** `mcp__<server>__<tool>` · `multi_tool_use.parallel` wrapper.

---

## 4. Antigravity (IDE + `agy` CLI)

**Sources:** `/Users/ninh.le/.local/bin/agy` (v1.1.10) · `Antigravity.app` `language_server` · hooks docs embedded: tool name = lowercase `CORTEX_STEP_TYPE_*` minus prefix.

App Cloud Antigravity agent API ([ai.google.dev](https://ai.google.dev/gemini-api/docs/antigravity-agent)) khác surface IDE: `code_execution`, `google_search`, `url_context`, filesystem via `environment`.

### High-confidence model tools (schemas / examples trong binary)

| Tool | Category |
| --- | --- |
| `run_command` | execute |
| `view_file` | read |
| `view_file_outline` | read |
| `view_code_item` | read |
| `write_to_file` | edit |
| `edit_file` | edit |
| `replace_file_content` / `multi_replace` | edit |
| `list_directory` | read |
| `find` / `find_by_name` | search |
| `grep_search` | search |
| `code_search` / `codebase_search` | search |
| `search_web` | web |
| `read_url_content` | web |
| `notify_user` | interaction |
| `ask_question` | interaction |
| `task_boundary` | tasks |
| `command_status` | execute |
| `send_command_input` | execute |
| `browser_subagent` | browser |
| `open_browser_url` | browser |
| `read_browser_page` | browser |
| `list_browser_pages` | browser |
| `capture_browser_screenshot` | browser |
| `capture_browser_console_logs` | browser |
| `execute_browser_javascript` | browser |
| `browser_*` family | browser | click/input/scroll/mouse/DOM/network… |
| `mcp_tool` | mcp |
| `list_resources` / `read_resource` | mcp/resources |
| `tool_search` | meta |
| `invoke_subagent` | orchestration |
| `generate_image` | media |
| `edit_notebook` / `read_notebook` / `execute_notebook` | notebook |
| `git_commit` | vcs |
| `wait` | sync |

### Full `CORTEX_STEP_TYPE_*` catalog (agy binary)

Bao gồm cả step nội bộ (system/UI), không phải tất cả model-callable. Tên tool = snake_case của enum.

<details>
<summary>Toàn bộ step types (đã lọc chuỗi dính lỗi parse)</summary>

`agency_tool_call`, `ask_question`, `blaze_build_targets`, `blaze_test_targets`, `brain_update`, `browser_click_element`, `browser_drag_pixel_to_pixel`, `browser_get_dom`, `browser_get_network_request`, `browser_input`, `browser_list_network_requests`, `browser_mouse_down`, `browser_mouse_up`, `browser_mouse_wheel`, `browser_move_mouse`, `browser_press_key`, `browser_refresh_page`, `browser_resize_window`, `browser_scroll`, `browser_scroll_down`, `browser_scroll_up`, `browser_select_option`, `browser_subagent`, `build_cleaner`, `capture_browser_console_logs`, `capture_browser_screenshot`, `checkpoint`, `cider_agent_dummy`, `click_browser_pixel`, `clipboard`, `cloud_sql_execute_sql`, `cloud_sql_update_schema`, `code_acknowledgement`, `code_action`, `code_search`, `command_status`, `compile`, `compile_applet`, `conversation_history`, `critique`, `delete_directory`, `deploy_firebase`, `directory_rules`, `dummy`, `edit_notebook`, `ephemeral_message`, `error_message`, `execute_browser_javascript`, `execute_notebook`, `file_change`, `find`, `findings`, `find_all_references`, `finish`, `generate_image`, `generic`, `git_commit`, `grep_search`, `install_applet_dependencies`, `install_applet_package`, `internal_search`, `invoke_subagent`, `ki_insertion`, `knowledge_artifacts`, `knowledge_generation`, `lint_applet`, `lint_diff`, `list_browser_pages`, `list_directory`, `list_resources`, `manager_feedback`, `mcp_tool`, `memory`, `moma`, `move`, `mquery`, `notify_user`, `open_browser_url`, `planner_response`, `plan_input`, `post_pr_review`, `proposal_feedback`, `propose_ai_comments`, `propose_code`, `read_browser_page`, `read_notebook`, `read_resource`, `read_terminal`, `read_url_content`, `restart_dev_server`, `retrieve_content`, `retrieve_memory`, `rpc_action`, `run_command`, `run_extension_code`, `search_web`, `send_command_input`, `set_up_cloud_sql`, `set_up_firebase`, `shell_exec`, `start_code_review`, `suggested_responses`, `system_message`, `task_boundary`, `tool_call_choice`, `tool_call_proposal`, `tool_search`, `trajectory_choice`, `trajectory_search`, `unspecified`, `user_input`, `view_code_item`, `view_content_chunk`, `view_file`, `view_file_outline`, `wait`, `workspace_api`, `write_blob`

</details>

**Hook matcher:** `"run_command|view_file"`, `"browser_.*"`.

---

## 5. Gemini CLI (liên quan Antigravity / Google)

**Sources:** [tools.md](https://github.com/google-gemini/gemini-cli/blob/main/docs/reference/tools.md) · `@google/gemini-cli@0.49.0`.

| Tool | Kind | Category |
| --- | --- | --- |
| `run_shell_command` | Execute | shell |
| `glob` | Search | fs |
| `grep_search` | Search | fs (alias legacy: `search_file_content`) |
| `list_directory` | Read | fs |
| `read_file` | Read | fs |
| `read_many_files` | Read | fs (`@` trigger) |
| `replace` | Edit | fs |
| `write_file` | Edit | fs |
| `ask_user` | Communicate | interaction |
| `write_todos` | Other | tasks |
| `tracker_*` | Think/Other | experimental task tracker |
| `update_topic` | Think | status |
| `list_mcp_resources` | Search | mcp |
| `read_mcp_resource` | Read | mcp |
| `activate_skill` | Other | skills |
| `get_internal_docs` | Think | self-docs |
| `enter_plan_mode` | Plan | |
| `exit_plan_mode` | Plan | |
| `complete_task` | Other | subagent-only |
| `google_web_search` | Search | web |
| `web_fetch` | Fetch | web |

Session: `/tools` · `/tools desc`.

---

## Cross-IDE map (core ops)

| Capability | Claude | Cursor | Codex | Antigravity | Gemini CLI |
| --- | --- | --- | --- | --- | --- |
| Shell | `Bash` / `PowerShell` | `Shell` | `shell_command` / `exec_command` | `run_command` | `run_shell_command` |
| Read file | `Read` | `Read` | shell / `read_file`† | `view_file` | `read_file` |
| Write/create | `Write` | `Write` | `apply_patch` | `write_to_file` | `write_file` |
| Edit | `Edit` | `StrReplace` / `ApplyPatch` | `apply_patch` | `edit_file` / replace* | `replace` |
| Glob | `Glob` | `Glob` | `glob_file_search`† | `find` | `glob` |
| Grep | `Grep` | `Grep` | `rg`† / shell | `grep_search` | `grep_search` |
| Web fetch | `WebFetch` | `WebFetch` | (search/hosted) | `read_url_content` | `web_fetch` |
| Web search | `WebSearch` | `WebSearch` | `web_search` | `search_web` | `google_web_search` |
| Plan | `Enter/ExitPlanMode` | `SwitchMode` / `CreatePlan` | `update_plan` | `plan_input` / planner* | `enter/exit_plan_mode` |
| Tasks | `Task*` / `TodoWrite` | `TodoWrite` / `Task` | `update_plan` | `task_boundary` | `write_todos` / tracker* |
| MCP | `List/ReadMcp*` + `mcp__*` | `CallMcpTool` + resources | `list/read_mcp_*` + `mcp__*` | `mcp_tool` | `list/read_mcp_*` |
| Subagent | `Agent` / `Workflow` | `Task` | `spawn_agent`… | `invoke_subagent` / `browser_subagent` | `complete_task` |

† KB / older Codex surface; current builds thường ưu tiên shell + `apply_patch`.

---

## amux implications

- Mid-layer (`pkg/tools`) chỉ cần **preserve dialect wire** — client tự execute.
- Inventory này = catalog tham chiếu khi map dialect / debug missing tool — không phải allowlist cứng trong proxy.
- HTML tables: [inventory.html](./inventory.html).
