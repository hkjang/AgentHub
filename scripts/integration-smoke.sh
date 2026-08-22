#!/usr/bin/env bash
set -euo pipefail

base_url="${AGENTHUB_TEST_URL:-http://127.0.0.1:18080}"
admin="${AGENTHUB_TEST_ADMIN:-testadmin}"
password="${AGENTHUB_TEST_PASSWORD:-integration-password-2026}"
database="${AGENTHUB_TEST_DATABASE:-agenthub_integration_0813}"
postgres_container="${AGENTHUB_TEST_POSTGRES_CONTAINER:-agenthub-postgres-1}"
test_dir=$(mktemp -d)
cookie="$test_dir/cookies"

# Every object this run creates carries the run's own suffix.
#
# The names used to be fixed, so a second run against the same deployment got a
# 409 on the first create and carried the string "null" forward as an id — it
# then asked for /api/v1/workspaces/null/snapshots, built an agent bound to a
# workspace called "null", and finally died on "Could not resolve host: null",
# which names DNS rather than the step that failed. The assertions at the end
# were right and were never reached. That is why the default database name has a
# date in it: the check could only ever be run once.
run="${AGENTHUB_TEST_RUN_ID:-$(date +%s)}"

status() {
  local method=$1 path=$2 body=$3 output=$4
  shift 4
  local args=(-sS -b "$cookie" -o "$output" -w '%{http_code}' -X "$method")
  if [[ "$method" != GET && "$method" != HEAD ]]; then
    args+=(-H "X-CSRF-Token: $csrf" -H 'Content-Type: application/json')
  fi
  if [[ -n "$body" ]]; then
    args+=(-d "$body")
  fi
  curl "${args[@]}" "$@" "$base_url$path"
}

# id reads an identifier a later step depends on, and stops here if it is not
# there. A missing id is the end of the run: everything after it would be asking
# the platform about something that does not exist, and the answers would be
# read as this check having covered them.
id() {
  local file=$1 field=$2 what=$3
  local value
  value=$(jq -r ".$field // empty" "$file")
  if [[ -z "$value" || "$value" == null ]]; then
    echo "$what did not come back with an id — the platform answered:" >&2
    head -c 400 "$file" >&2
    echo >&2
    exit 1
  fi
  printf '%s' "$value"
}

login_body=$(jq -cn --arg username "$admin" --arg password "$password" '{username:$username,password:$password}')
login_code=$(curl -sS -c "$cookie" -o "$test_dir/login.json" -w '%{http_code}' -H 'Content-Type: application/json' -d "$login_body" "$base_url/api/v1/auth/login")
csrf=$(awk '$6=="agenthub_csrf" {print $7}' "$cookie")
[[ "$login_code" == 200 && -n "$csrf" ]]

csrf_reject=$(curl -sS -b "$cookie" -o "$test_dir/csrf.json" -w '%{http_code}' -H 'Content-Type: application/json' -X PUT -d '{"value":{}}' "$base_url/api/v1/admin/settings/sessionGateway")
settings=$(status PUT /api/v1/admin/settings/sessionGateway '{"value":{"enabled":true,"scheme":"http","baseDomain":"localhost:18080","sessionHours":8}}' "$test_dir/settings.json")
runtime_env=$(status PUT /api/v1/admin/settings/runtimeEnvironment '{"value":{"files":[{"path":"/etc/pip.conf","content":"[global]\nindex-url = https://nexus.company.local/repository/pypi-all/simple\n","mode":"0644","enabled":true}],"variables":[{"name":"PIP_INDEX_URL","value":"https://nexus.company.local/repository/pypi-all/simple","enabled":true}]}}' "$test_dir/runtime-env.json")
# A path the platform owns has to be refused on the way in, not dropped later.
runtime_env_reject=$(status PUT /api/v1/admin/settings/runtimeEnvironment '{"value":{"files":[{"path":"/etc/agenthub/runtime.json","content":"x"}],"variables":[]}}' "$test_dir/runtime-env-reject.json")
workspace=$(status POST /api/v1/workspaces "{\"name\":\"integration-workspace-$run\",\"type\":\"empty\",\"sizeGb\":10}" "$test_dir/workspace.json")
workspace_id=$(id "$test_dir/workspace.json" id "the workspace")
snapshot=$(status POST "/api/v1/workspaces/$workspace_id/snapshots" "{\"name\":\"integration-snapshot-$run\"}" "$test_dir/snapshot.json")

agent_body=$(jq -cn --arg workspace "$workspace_id" --arg run "$run" '{name:("Integration Agent "+$run),description:"end-to-end verification",runtimeType:"opencode",runtimeProfileId:"rp-basic",workspaceId:$workspace,systemPrompt:"Operate safely"}')
agent=$(status POST /api/v1/agents "$agent_body" "$test_dir/agent.json")
agent_id=$(id "$test_dir/agent.json" id "the agent")
spawn=$(status POST "/api/v1/agents/$agent_id/spawn" '{}' "$test_dir/spawn.json")
runtime_id=$(id "$test_dir/spawn.json" runtime.id "the spawned runtime")

workflow_body=$(jq -cn --arg agent "$agent_id" --arg run "$run" '{name:("Integration Workflow "+$run),description:"DAG validation",mode:"sequential",maxDepth:4,maxAgentCalls:12,maxToolCalls:50,maxDurationSeconds:900,maxParallelAgents:3,definition:{steps:[{id:"build",agentId:$agent,dependsOn:[]},{id:"review",agentId:$agent,dependsOn:["build"]}]},enabled:true}')
workflow=$(status POST /api/v1/workflows "$workflow_body" "$test_dir/workflow.json")
workflow_id=$(id "$test_dir/workflow.json" id "the workflow")
workflow_validate=$(status POST "/api/v1/workflows/$workflow_id/validate" '{}' "$test_dir/workflow-validate.json")
cycle_body=$(jq -cn --arg agent "$agent_id" --arg run "$run" '{name:("Invalid Cycle "+$run),mode:"sequential",maxDepth:4,maxAgentCalls:12,maxToolCalls:50,maxDurationSeconds:900,maxParallelAgents:3,definition:{steps:[{id:"a",agentId:$agent,dependsOn:["b"]},{id:"b",agentId:$agent,dependsOn:["a"]}]}}')
cycle_reject=$(status POST /api/v1/workflows "$cycle_body" "$test_dir/cycle.json")

evaluation_set_body=$(jq -cn --arg run "$run" '{name:("Integration Preflight "+$run),description:"required bindings",passThreshold:100,cases:[{name:"OpenCode runtime",expectedRuntime:"opencode"},{name:"Profile",requiresProfile:true},{name:"Workspace",requiresWorkspace:true},{name:"Security",requiresSecurity:true}]}')
evaluation_set=$(status POST /api/v1/evaluation/test-sets "$evaluation_set_body" "$test_dir/evaluation-set.json")
evaluation_set_id=$(id "$test_dir/evaluation-set.json" id "the evaluation set")
evaluation=$(status POST "/api/v1/agents/$agent_id/evaluate" "{\"testSetId\":\"$evaluation_set_id\"}" "$test_dir/evaluation.json")

secret=$(status POST /api/v1/secrets "{\"name\":\"Integration Secret $run\",\"kind\":\"api_key\",\"value\":\"never-return-this-value\"}" "$test_dir/secret.json")
rotate=$(status POST /api/v1/keys/rotate '{}' "$test_dir/rotate.json")
api_key=$(status POST /api/v1/api-keys "{\"name\":\"Integration MCP $run\",\"scopes\":[\"api:read\",\"mcp:read\",\"runtime:manage\"]}" "$test_dir/api-key.json")
api_token=$(id "$test_dir/api-key.json" token "the API key")

mcp=$(curl -sS -o "$test_dir/mcp.json" -w '%{http_code}' -H "Authorization: Bearer $api_token" -H 'Content-Type: application/json' -H 'MCP-Protocol-Version: 2026-07-28' -H 'Mcp-Method: tools/list' -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}' "$base_url/mcp")
mcp_mismatch=$(curl -sS -o "$test_dir/mcp-mismatch.json" -w '%{http_code}' -H "Authorization: Bearer $api_token" -H 'Content-Type: application/json' -H 'MCP-Protocol-Version: 2026-07-28' -H 'Mcp-Method: wrong/method' -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}' "$base_url/mcp")
openapi=$(status GET /api/openapi.json '' "$test_dir/openapi.json")
logs=$(status GET '/api/v1/admin/logs?limit=20' '' "$test_dir/logs.json")
notifications=$(status GET /api/v1/notifications '' "$test_dir/notifications.json")

docker exec "$postgres_container" psql -U agenthub -d "$database" -c "UPDATE agent_runtimes SET status='ready' WHERE id='$runtime_id'" >/dev/null
launch=$(status POST "/api/v1/runtimes/$runtime_id/launch" '{}' "$test_dir/launch.json")
launch_url=$(id "$test_dir/launch.json" url "the launch")
gateway_without_k8s=$(curl --noproxy '*' -sS -o "$test_dir/gateway.json" -w '%{http_code}' "$launch_url")
ticket_replay=$(curl --noproxy '*' -sS -o "$test_dir/replay.json" -w '%{http_code}' "$launch_url")
# Without a Runtime Base Domain the same session is served from the Portal's own
# origin, so clearing it must change the launch URL rather than refuse a launch.
gateway_path_mode=$(status PUT /api/v1/admin/settings/sessionGateway '{"value":{"enabled":false,"scheme":"http","baseDomain":"","sessionHours":8}}' "$test_dir/gateway-path.json")
sleep 6
path_launch=$(status POST "/api/v1/runtimes/$runtime_id/launch" '{}' "$test_dir/path-launch.json")
path_mode=$(jq -r '.mode' "$test_dir/path-launch.json")
path_url=$(jq -r '.url' "$test_dir/path-launch.json")
path_unauthorized=$(curl --noproxy '*' -sS -o "$test_dir/path-unauthorized.html" -w '%{http_code}' "$base_url/$runtime_id/")
path_ticket=$(curl --noproxy '*' -sS -o "$test_dir/path-ticket.json" -w '%{http_code}' "$base_url$path_url")
portal_intact=$(curl --noproxy '*' -sS -b "$cookie" -o "$test_dir/portal.json" -w '%{http_code}' "$base_url/api/v1/me")
migrations=$(docker exec "$postgres_container" psql -U agenthub -d "$database" -Atc 'SELECT max(version) FROM schema_migrations')
# What the binary should have applied, read from the tree rather than pinned to a
# number: the literal that used to be here went stale the first time a migration
# was added, and a stale assertion is worse than none — it fails for the wrong
# reason and gets ignored.
expected_migrations=$(ls "$(dirname "$0")/../internal/store/migrations"/*.sql | sed 's#.*/##; s/_.*//' | sed 's/^0*//' | sort -n | tail -1)

printf 'login=%s csrf_reject=%s settings=%s workspace=%s snapshot=%s agent=%s spawn=%s\n' "$login_code" "$csrf_reject" "$settings" "$workspace" "$snapshot" "$agent" "$spawn"
printf 'workflow=%s workflow_validate=%s cycle_reject=%s evaluation_set=%s evaluation=%s score=%s\n' "$workflow" "$workflow_validate" "$cycle_reject" "$evaluation_set" "$evaluation" "$(jq -r '.score' "$test_dir/evaluation.json")"
printf 'secret=%s rotate=%s key_version=%s api_key=%s mcp=%s mcp_tools=%s mcp_mismatch=%s\n' "$secret" "$rotate" "$(jq -r '.version' "$test_dir/rotate.json")" "$api_key" "$mcp" "$(jq '.result.tools|length' "$test_dir/mcp.json")" "$mcp_mismatch"
printf 'openapi=%s logs=%s notifications=%s launch=%s gateway_without_k8s=%s ticket_replay=%s migrations=%s/%s\n' "$openapi" "$logs" "$notifications" "$launch" "$gateway_without_k8s" "$ticket_replay" "$migrations" "$expected_migrations"
printf 'runtime_env=%s runtime_env_reject=%s path_mode=%s path_url=%s path_unauthorized=%s path_ticket=%s portal_intact=%s\n' "$runtime_env" "$runtime_env_reject" "$path_mode" "$path_url" "$path_unauthorized" "$path_ticket" "$portal_intact"

[[ "$csrf_reject" == 403 && "$settings" == 200 && "$workspace" == 201 && "$snapshot" == 202 ]]
[[ "$agent" == 201 && "$spawn" == 202 && "$workflow" == 200 && "$workflow_validate" == 200 && "$cycle_reject" == 400 ]]
[[ "$evaluation" == 200 && $(jq -r '.score' "$test_dir/evaluation.json") == 100 ]]
[[ "$secret" == 201 && "$rotate" == 200 && "$api_key" == 201 ]]
[[ "$mcp" == 200 && $(jq '.result.tools|length' "$test_dir/mcp.json") == 3 && "$mcp_mismatch" == 400 ]]
[[ "$openapi" == 200 && "$logs" == 200 && "$notifications" == 200 ]]
[[ "$launch" == 201 && "$gateway_without_k8s" == 503 && "$ticket_replay" == 401 ]]
[[ "$migrations" == "$expected_migrations" ]] || { echo "applied migration $migrations, expected $expected_migrations" >&2; exit 1; }
[[ "$runtime_env" == 200 && "$runtime_env_reject" == 400 ]]
# 503 rather than 200 because this smoke run has no cluster: the ticket was
# accepted and the runtime endpoint is what could not be reached.
[[ "$gateway_path_mode" == 200 && "$path_mode" == path && "$path_url" == "/$runtime_id/?ticket="* ]]
[[ "$path_unauthorized" == 401 && "$path_ticket" == 503 && "$portal_intact" == 200 ]]
