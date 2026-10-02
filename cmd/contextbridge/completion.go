package main

import (
	"errors"
	"fmt"
	"strings"
)

var completionRootCommands = []string{
	"init", "serve", "run", "stop", "console", "submit", "schedule", "result", "review",
	"health", "dashboard", "status", "doctor", "guide", "hardware", "models", "resources", "uninstall",
	"pull", "runtime", "mcp", "integrate", "adapter", "benchmark", "verification", "relay", "pair", "worker", "cluster", "route", "selftest", "update", "completion", "version", "help",
}

var completionSubcommands = map[string][]string{
	"schedule":     {"add", "list", "show", "pause", "resume", "run", "delete"},
	"runtime":      {"install"},
	"mcp":          {"serve"},
	"integrate":    {"openai", "litellm", "mcp", "relay", "ui"},
	"adapter":      {"list", "details", "doctor", "setup", "conformance", "enable", "disable", "start", "stop"},
	"verification": {"verify"},
	"cluster":      {"status", "events", "estimate", "node", "protocol", "conformance", "submit", "chat", "agent", "selftest", "route", "contract", "receipt", "login", "account", "token", "pairing", "pool", "configure", "dashboard", "pipeline", "lan"},
	"route":        {"explain"},
	"update":       {"status", "check", "apply", "enable", "disable", "auto"},
	"completion":   {"powershell", "bash", "zsh"},
	"uninstall":    {"--config", "--install-dir", "--purge", "--force", "--yes", "--dry-run"},
}

// completionCommand emits static, auditable shell integration. It deliberately
// never reads a config, token, prompt, model response, or network resource.
// Installers may save this output in the user's normal completion directory;
// operators can also source/evaluate it manually on an uninstalled system.
func completionCommand(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: contextbridge completion powershell|bash|zsh")
	}
	var script string
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "powershell", "pwsh":
		script = powershellCompletionScript()
	case "bash":
		script = bashCompletionScript()
	case "zsh":
		script = zshCompletionScript()
	default:
		return fmt.Errorf("unsupported shell %q; use powershell, bash, or zsh", args[0])
	}
	fmt.Print(script)
	return nil
}

func powershellCompletionScript() string {
	root := quotePowerShellList(completionRootCommands)
	return fmt.Sprintf(`# ContextBridge native completion for both command names.
$script:ContextBridgeRootCommands = @(%s)
$script:ContextBridgeSubcommands = @{
    schedule = @('add','list','show','pause','resume','run','delete')
    runtime = @('install')
    mcp = @('serve')
    integrate = @('openai','litellm','mcp','relay','ui')
	adapter = @('list','details','doctor','setup','conformance','enable','disable','start','stop')
    verification = @('verify')
	cluster = @('status','events','estimate','node','protocol','conformance','submit','chat','scheduled-action','agent','selftest','route','contract','receipt','login','account','token','pairing','pool','configure','dashboard','pipeline','lan')
    route = @('explain')
    update = @('status','check','apply','enable','disable','auto')
    completion = @('powershell','bash','zsh')
}
$script:ContextBridgeOptions = @{
    'init' = @('--config')
    'serve' = @('--config')
    'run' = @('--config','--slots','--topmost')
    'stop' = @('--config','--force')
    'uninstall' = @('--config','--install-dir','--purge','--force','--yes','--dry-run')
    'console' = @('--config','--token')
    'submit' = @('--file','--artifacts','--config')
    'schedule' = @('--file','--config','--interactive')
    'result' = @('--config')
    'review' = @('--job-dir','--config')
    'health' = @('--config')
    'dashboard' = @('--config','--no-open')
    'status' = @('--config','--json')
    'doctor' = @('--config','--json')
    'guide' = @('--config')
    'hardware' = @('--json')
    'models' = @('--config','--json','--discover')
    'resources' = @('--config','--json')
    'pull' = @('--config')
    'runtime install' = @('--config')
    'mcp serve' = @('--config')
    'integrate openai' = @('--config','--json','--show-token','--write-env','--check','--live')
    'integrate litellm' = @('--config','--json','--write-config','--write-env')
    'integrate mcp' = @('--config','--json')
    'integrate relay' = @('--config','--json','--write-env','--subject','--groups','--lifetime-hours','--max-queued-jobs','--max-jobs-per-hour','--max-priority','--providers','--allowed-tenants','--egress','--require-e2ee','--scheduled-actions-policy')
    'integrate ui' = @('--config','--json','--write-env','--subject','--lifetime-hours','--allowed-subjects','--allowed-tenants')
	'adapter list' = @('--config','--account','--token','--token-file','--json')
	'adapter details' = @('--config','--account','--token','--token-file','--json')
	'adapter doctor' = @('--config','--account','--token','--token-file','--json')
	'adapter setup' = @('--config','--label','--driver','--route','--task','--model','--timeout-seconds','--principal','--token-file','--classification','--option','--create-token','--json')
	'adapter conformance' = @('--adapter','--arg','--profile','--profile-file','--job-file','--working-directory','--timeout-seconds','--json')
	'adapter enable' = @('--config','--account','--token','--token-file','--json')
	'adapter disable' = @('--config','--account','--token','--token-file','--json')
	'adapter start' = @('--config','--account','--token','--token-file','--json')
	'adapter stop' = @('--config','--account','--token','--token-file','--json')
    'benchmark' = @('--json','--samples','--warmup','--database-jobs','--idle-duration','--binary')
    'verification verify' = @('--file','--trust-key','--artifact','--require-artifact','--evidence-dir','--require-evidence','--json')
    'relay' = @('--config')
	'pair' = @('--config','--relay','--identity','--name','--interactive')
    'worker' = @('--config','--relay','--identity','--name','--slots','--providers','--models','--tasks','--groups','--no-updates','--topmost')
    'cluster status' = @('--config','--json')
	'cluster events' = @('--config','--token','--after','--limit','--json','--follow','--pipeline','--poll')
	'cluster estimate' = @('--config','--token','--json')
	'cluster node' = @('drain','resume','--config','--token','--json')
	'cluster protocol' = @('--config','--token','--json')
	'cluster conformance' = @('relay','worker','resilience','--config','--token','--node','--json')
    'cluster submit' = @('--config','--account','--file','--token','--wait','--e2ee','--stream','--artifacts','--idempotency-key','--interactive')
    'cluster chat' = @('--config','--account','--token','--provider','--group','--model','--profile','--reasoning','--e2ee','--session','--prompt','--artifacts','--min-artifacts','--image','--min-images','--attach-image','--new-session','--new-session-per-job','--foreground-new-session','--egress','--max-cost-usd')
	'cluster scheduled-action' = @('preview','confirm','list','show','cancel','--config','--account','--token','--token-file','--file','--status','--limit','--json')
    'cluster agent' = @('plan','run','auto','--config','--token','--goal','--goal-file','--policy','--planner-provider','--planner-profile','--planner-model','--allow-providers','--allow-adapter-profiles','--max-steps','--step-timeout','--max-runtime','--planner-timeout','--out','--plan','--approve')
	'cluster selftest' = @('--config','--providers','--local-model','--run','--dry-run','--image','--image-profile','--artifacts','--keep-artifacts','--timeout','--job-timeout','--poll')
    'cluster route' = @('explain','--config','--account','--file','--job','--token','--json')
	'cluster contract' = @('validate','--config','--file','--token','--json')
	'cluster receipt' = @('show','export','verify','keygen','--config','--token','--out','--file','--signing-key','--trust-key','--offline','--private-out','--public-out','--key-id','--issuer')
    'route explain' = @('--config','--file','--job','--token','--json')
	'selftest' = @('--config','--providers','--local-model','--run','--dry-run','--image','--image-profile','--artifacts','--keep-artifacts','--timeout','--job-timeout','--poll')
    'cluster configure' = @('--config','--mode','--relay-url','--public-url','--name','--listen','--interactive')
    'cluster dashboard' = @('--config','--no-open')
    'cluster pipeline' = @('activity','--config','--name','--file','--token','--json')
    'cluster login' = @('--config','--token-file','--account','--relay','--pool-authority-file','--activate')
	'cluster account' = @('list','use','remove','--config','--json')
	'cluster token' = @('create','list','revoke','--config','--role','--subject','--groups','--lifetime-hours','--max-queued-jobs','--max-jobs-per-hour','--max-priority','--providers','--allowed-tenants','--allowed-subjects','--egress','--require-e2ee','--scheduled-actions-policy')
	'cluster token create' = @('--config','--role','--subject','--groups','--lifetime-hours','--max-queued-jobs','--max-jobs-per-hour','--max-priority','--providers','--allowed-tenants','--allowed-subjects','--egress','--require-e2ee','--scheduled-actions-policy')
	'cluster token list' = @('--config','--token','--limit','--offset','--json')
	'cluster token revoke' = @('--config','--token','--json')
	'cluster pairing' = @('--config','--approve','--deny')
	'cluster pool' = @('init','--id','--out')
	'cluster lan' = @('init','relocate','join','status','--config','--listen','--advertise-host','--certificate-out','--out','--bundle','--name')
	'cluster lan init' = @('--config','--listen','--advertise-host','--out')
	'cluster lan relocate' = @('--config','--listen','--advertise-host','--certificate-out','--out')
	'cluster lan join' = @('--config','--bundle','--name')
	'cluster lan status' = @('--config')
    'update' = @('--config','--force','--json','--managed-service','--relay-only')
}
$script:ContextBridgeValueOptions = @{
    '--provider' = @('adapter','ollama','nuextract','jina')

    '--reasoning' = @('instant','medium','high','xhigh','pro','max')
    '--mode' = @('local','client','relay','worker','all')
	'--role' = @('admin','producer','node','observer')
	'--max-priority' = @('0','20','40','60','80','100')
	'--classification' = @('local','remote')
}
$script:ContextBridgeTakesValue = @(
	'--config','--install-dir','--file','--job','--artifacts','--artifact','--attach-image','--identity','--job-dir','--token-file','--pool-authority-file','--trust-key','--evidence-dir','--write-env','--write-config','--bundle','--advertise-host','--certificate-out','--scheduled-actions-policy',
    '--slots','--endpoint','--relay','--name','--providers','--models','--tasks','--groups',
    '--token','--account','--provider','--group','--model','--profile','--reasoning','--session','--prompt',
	'--min-artifacts','--min-images','--local-model','--image-profile','--timeout','--job-timeout','--poll','--after','--limit','--offset','--idempotency-key','--subject','--groups','--allowed-tenants','--allowed-subjects','--lifetime-hours','--max-queued-jobs','--max-jobs-per-hour','--max-priority',
	'--mode','--relay-url','--public-url','--listen','--role','--subject','--approve','--deny','--label','--driver','--route','--task','--model','--timeout-seconds','--principal','--classification','--option','--adapter','--arg','--profile-file','--job-file','--working-directory',
    '--managed-service','--samples','--warmup','--database-jobs','--idle-duration','--binary',
    '--goal','--goal-file','--planner-provider','--planner-profile','--planner-model','--allow-providers',
    '--policy','--allow-adapter-profiles','--max-steps','--step-timeout','--max-runtime','--planner-timeout','--out','--plan','--approve'
)
Register-ArgumentCompleter -Native -CommandName contextbridge, cb -ScriptBlock {
    param($wordToComplete, $commandAst, $cursorPosition)
    $elements = @($commandAst.CommandElements | Select-Object -Skip 1)
    $tokens = @($elements | ForEach-Object { $_.Extent.Text.Trim([char[]]@([char]39,[char]34,[char]96)) })
    $hasCurrentToken = $elements.Count -gt 0 -and $elements[$elements.Count - 1].Extent.EndOffset -eq $cursorPosition
    # Do not assign an array through an if expression here. PowerShell
    # enumerates a one-item result and can collapse it into a scalar string;
    # indexing that value then returns its first character instead of the
    # command name. Keep the collection explicitly typed so partial options
    # (--r followed by Endpoint) and partial nested actions work as well as trailing spaces.
    [string[]]$completed = @($tokens)
    if ($hasCurrentToken) {
        [string[]]$completed = if ($tokens.Count -le 1) { @() } else { @($tokens[0..($tokens.Count - 2)]) }
    }
    $command = if ($completed.Count -ge 1) { $completed[0] } else { '' }
    $subcommand = if ($completed.Count -ge 2 -and $script:ContextBridgeSubcommands.ContainsKey($command) -and -not $completed[1].StartsWith('-')) { $completed[1] } else { '' }
    $key = if ($subcommand) { "$command $subcommand" } else { $command }
    if (($key -eq 'cluster lan' -or $key -eq 'cluster token') -and $completed.Count -ge 3 -and -not $completed[2].StartsWith('-')) {
        $key = "$key $($completed[2])"
    }
    $previous = if ($completed.Count -ge 1) { $completed[$completed.Count - 1] } else { '' }
    $expectsValue = $script:ContextBridgeTakesValue -contains $previous

    if ($expectsValue) {
        $candidates = @($script:ContextBridgeValueOptions[$previous])
    } elseif (-not $command) {
        $candidates = $script:ContextBridgeRootCommands
    } elseif ($script:ContextBridgeSubcommands.ContainsKey($command) -and -not $subcommand) {
        $candidates = $script:ContextBridgeSubcommands[$command]
    } elseif ($wordToComplete.StartsWith('-') -or -not $wordToComplete) {
        $candidates = @($script:ContextBridgeOptions[$command]) + @($script:ContextBridgeOptions[$key])
    } else {
        $candidates = @()
    }
    $candidates | Sort-Object -Unique | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
        [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
    }
}
`, root)
}

func quotePowerShellList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "'"+strings.ReplaceAll(value, "'", "''")+"'")
	}
	return strings.Join(quoted, ",")
}

func bashCompletionScript() string {
	return `# ContextBridge managed completion
# Native completion for contextbridge and cb.
_contextbridge_complete() {
  local current previous command subcommand option_key candidates boolean_previous
  current="${COMP_WORDS[COMP_CWORD]}"
  previous="${COMP_WORDS[COMP_CWORD-1]}"
  command="${COMP_WORDS[1]:-}"
  subcommand=""
  option_key="$command"
  boolean_previous=0
  if (( COMP_CWORD > 2 )); then
    subcommand="${COMP_WORDS[2]:-}"
    case "$command" in
      schedule|runtime|mcp|integrate|adapter|verification|cluster|route|update)
        if [[ -n "$subcommand" && "$subcommand" != -* ]]; then
          option_key="$command $subcommand"
        fi
        ;;
    esac
  fi
  if [[ ( "$option_key" == "cluster lan" || "$option_key" == "cluster token" ) && "$COMP_CWORD" -gt 3 && -n "${COMP_WORDS[3]:-}" && "${COMP_WORDS[3]}" != -* ]]; then
    option_key="$option_key ${COMP_WORDS[3]}"
  fi

  case "$previous" in
	--working-directory)
	  if declare -F _filedir >/dev/null 2>&1; then _filedir -d; else COMPREPLY=( $(compgen -d -- "$current") ); fi
	  return ;;
	--config|--file|--artifacts|--artifact|--attach-image|--identity|--job-dir|--token-file|--pool-authority-file|--binary|--trust-key|--evidence-dir|--write-env|--write-config|--certificate-out|--out|--bundle|--adapter|--profile-file|--job-file|--scheduled-actions-policy)
      if declare -F _filedir >/dev/null 2>&1; then _filedir; else COMPREPLY=( $(compgen -f -- "$current") ); fi
      return ;;
    --provider) candidates="adapter ollama nuextract jina" ;;

    --reasoning) candidates="instant medium high xhigh pro max" ;;
    --mode) candidates="local client relay worker all" ;;
    --role) candidates="admin producer node observer" ;;
	--max-priority) candidates="0 20 40 60 80 100" ;;
	--classification) candidates="local remote" ;;
    --wait|--e2ee|--require-e2ee|--stream|--json|--follow|--pipeline|--interactive|--show-token|--check|--live|--no-open|--topmost|--image|--new-session|--new-session-per-job|--foreground-new-session|--run|--dry-run|--keep-artifacts|--no-updates|--discover|--force|--relay-only|--require-artifact|--require-evidence|--create-token) boolean_previous=1 ;;
  esac
  if [ -n "${candidates:-}" ]; then
    COMPREPLY=( $(compgen -W "$candidates" -- "$current") )
    return
  fi

  if [[ "$current" == -* || ( "$boolean_previous" -eq 1 && -z "$current" ) ]]; then
    candidates=""
    case "$option_key" in
      "cluster chat") candidates="--config --account --token --provider --group --model --profile --reasoning --e2ee --session --prompt --artifacts --min-artifacts --image --min-images --attach-image --new-session --new-session-per-job --foreground-new-session --egress --max-cost-usd" ;;
	  "cluster scheduled-action") candidates="preview confirm list show cancel --config --account --token --token-file --file --status --limit --json" ;;
      "cluster agent") candidates="plan run auto --config --token --goal --goal-file --policy --planner-provider --planner-profile --planner-model --allow-providers --allow-adapter-profiles --max-steps --step-timeout --max-runtime --planner-timeout --out --plan --approve" ;;
	  "cluster selftest") candidates="--config --providers --local-model --run --dry-run --image --image-profile --artifacts --keep-artifacts --timeout --job-timeout --poll" ;;
	  "cluster route") candidates="--config --account --file --job --token --json" ;;
	  "cluster contract") candidates="validate --config --file --token --json" ;;
	  "cluster receipt") candidates="show export verify keygen --config --token --out --file --signing-key --trust-key --offline --private-out --public-out --key-id --issuer" ;;
      "route explain") candidates="--config --file --job --token --json" ;;
      "cluster submit") candidates="--config --account --file --token --wait --e2ee --stream --artifacts --idempotency-key --interactive" ;;
      "cluster status") candidates="--config --json" ;;
	  "cluster events") candidates="--config --token --after --limit --json --follow --pipeline --poll" ;;
	  "cluster estimate") candidates="--config --token --json" ;;
	  "cluster node") candidates="drain resume --config --token --json" ;;
	  "cluster protocol") candidates="--config --token --json" ;;
	  "cluster conformance") candidates="relay worker resilience --config --token --node --json" ;;
      "cluster configure") candidates="--config --mode --relay-url --public-url --name --listen --interactive" ;;
      "cluster dashboard") candidates="--config --no-open" ;;
      "cluster pipeline") candidates="activity --config --name --file --token --json" ;;
      "cluster login") candidates="--config --token-file --account --relay --pool-authority-file --activate" ;;
	  "cluster account") candidates="list use remove --config --json" ;;
	  "cluster token") candidates="create list revoke --config --role --subject --groups --lifetime-hours --max-queued-jobs --max-jobs-per-hour --max-priority --providers --allowed-tenants --allowed-subjects --egress --require-e2ee --scheduled-actions-policy" ;;
	  "cluster token create") candidates="--config --role --subject --groups --lifetime-hours --max-queued-jobs --max-jobs-per-hour --max-priority --providers --allowed-tenants --allowed-subjects --egress --require-e2ee --scheduled-actions-policy" ;;
	  "cluster token list") candidates="--config --token --limit --offset --json" ;;
	  "cluster token revoke") candidates="--config --token --json" ;;
	  "cluster pairing") candidates="--config --approve --deny" ;;
	  "cluster pool") candidates="init --id --out" ;;
	  "cluster lan") candidates="init relocate join status --config --listen --advertise-host --certificate-out --out --bundle --name" ;;
	  "cluster lan init") candidates="--config --listen --advertise-host --out" ;;
	  "cluster lan relocate") candidates="--config --listen --advertise-host --certificate-out --out" ;;
	  "cluster lan join") candidates="--config --bundle --name" ;;
	  "cluster lan status") candidates="--config" ;;
      "mcp serve") candidates="--config" ;;
      "integrate openai") candidates="--config --json --show-token --write-env --check --live" ;;
      "integrate litellm") candidates="--config --json --write-config --write-env" ;;
      "integrate mcp") candidates="--config --json" ;;
      "integrate relay") candidates="--config --json --write-env --subject --groups --lifetime-hours --max-queued-jobs --max-jobs-per-hour --max-priority --providers --allowed-tenants --egress --require-e2ee --scheduled-actions-policy" ;;
      "integrate ui") candidates="--config --json --write-env --subject --lifetime-hours --allowed-subjects --allowed-tenants" ;;
	  "adapter list"|"adapter details"|"adapter doctor"|"adapter enable"|"adapter disable"|"adapter start"|"adapter stop") candidates="--config --account --token --token-file --json" ;;
	  "adapter setup") candidates="--config --label --driver --route --task --model --timeout-seconds --principal --token-file --classification --option --create-token --json" ;;
	  "adapter conformance") candidates="--adapter --arg --profile --profile-file --job-file --working-directory --timeout-seconds --json" ;;
      "verification verify") candidates="--file --trust-key --artifact --require-artifact --evidence-dir --require-evidence --json" ;;
      benchmark) candidates="--json --samples --warmup --database-jobs --idle-duration --binary" ;;
      "schedule add") candidates="--config --file --interactive" ;;
      "schedule "*) candidates="--config --file" ;;
      console) candidates="--config --token" ;;
      init|serve|result|health|guide|pull|relay) candidates="--config" ;;
      run) candidates="--config --slots --topmost" ;;
      stop) candidates="--config --force" ;;
      uninstall) candidates="--config --install-dir --purge --force --yes --dry-run" ;;
      submit) candidates="--config --file --artifacts" ;;
      review) candidates="--config --job-dir" ;;
      dashboard) candidates="--config --no-open" ;;
      pair) candidates="--config --relay --identity --name --interactive" ;;
      worker) candidates="--config --relay --identity --name --slots --providers --models --tasks --groups --no-updates --topmost" ;;
      status|doctor|resources) candidates="--config --json" ;;
	  selftest) candidates="--config --providers --local-model --run --dry-run --image --image-profile --artifacts --keep-artifacts --timeout --job-timeout --poll" ;;
      models) candidates="--config --json --discover" ;;
      hardware) candidates="--json" ;;
      update|"update "*) candidates="--config --force --json --managed-service --relay-only" ;;
    esac
  elif [[ "$option_key" == "cluster route" && "$COMP_CWORD" -eq 3 ]]; then
    candidates="explain"
	elif [[ "$option_key" == "cluster contract" && "$COMP_CWORD" -eq 3 ]]; then
	  candidates="validate"
	elif [[ "$option_key" == "cluster conformance" && "$COMP_CWORD" -eq 3 ]]; then
	  candidates="relay worker resilience"
  elif [[ "$option_key" == "cluster agent" && "$COMP_CWORD" -eq 3 ]]; then
    candidates="plan run auto"
	elif [[ "$option_key" == "cluster receipt" && "$COMP_CWORD" -eq 3 ]]; then
	  candidates="show export verify keygen"
	elif [[ "$option_key" == "cluster scheduled-action" && "$COMP_CWORD" -eq 3 ]]; then
	  candidates="preview confirm list show cancel"
	elif [[ "$option_key" == "cluster lan" && "$COMP_CWORD" -eq 3 ]]; then
	  candidates="init relocate join status"
  elif [ "$COMP_CWORD" -eq 1 ]; then
    candidates="init serve run stop uninstall console submit schedule result review health dashboard status doctor guide hardware models resources pull runtime mcp integrate adapter benchmark verification relay pair worker cluster route selftest update completion version help"
  elif [ "$COMP_CWORD" -eq 2 ]; then
    case "$command" in
      schedule) candidates="add list show pause resume run delete" ;;
      runtime) candidates="install" ;;
      mcp) candidates="serve" ;;
      integrate) candidates="openai litellm mcp relay ui" ;;
	  adapter) candidates="list details doctor setup conformance enable disable start stop" ;;
      verification) candidates="verify" ;;
	  cluster) candidates="status events estimate node protocol conformance submit chat scheduled-action agent selftest route contract receipt login account token pairing pool configure dashboard pipeline lan" ;;
      route) candidates="explain" ;;
      update) candidates="status check apply enable disable auto" ;;
      completion) candidates="powershell bash zsh" ;;
      *) candidates="" ;;
    esac
  else
    candidates=""
  fi
  COMPREPLY=( $(compgen -W "$candidates" -- "$current") )
}
complete -o default -F _contextbridge_complete contextbridge cb
`
}

func zshCompletionScript() string {
	return `#compdef contextbridge cb
# ContextBridge managed completion
local -a root config
root=(
    'init:Create a starter configuration'
    'serve:Run only the local bridge service'
    'run:Run the local service and worker'
    'stop:Safely stop the local ContextBridge process'
    'uninstall:Remove owned program files and optionally managed data'
    'console:Attach a live terminal to the running service'
    'submit:Submit a local JSON job'
    'schedule:Manage durable schedules'
    'result:Read a saved job result'
    'review:Review a saved decision'
    'health:Read local health'
    'dashboard:Open the local dashboard'
    'status:Show local status'
    'doctor:Check setup and connectivity'
    'guide:Interactively choose and configure this device role'
    'hardware:Show detected hardware'
    'models:Show model inventory'
    'resources:Show detected portable resource packs'
    'pull:Download a managed model'
    'runtime:Manage local runtimes'
    'mcp:Expose bounded local tools over MCP stdio'
    'integrate:Generate safe application connection settings'
	'adapter:Inspect, configure, and gate optional external adapters'
    'benchmark:Measure bridge-only overhead and resource footprint'
    'verification:Verify signed, time-bounded interoperability statements'
    'relay:Run a relay'
    'pair:Pair this worker'
    'worker:Run a worker'
    'cluster:Use a remote pool'
    'route:Explain a preview or durable cluster route'
    'selftest:Wait for and optionally run safe pool checks'
    'update:Manage verified updates'
    'completion:Generate shell completion'
    'version:Print the version'
    'help:Show command help'
)
config=('--config[Configuration file]:configuration file:_files')
if (( CURRENT == 2 )); then
  _describe 'ContextBridge command' root
  return
fi
case "$words[2]" in
  schedule)
    if (( CURRENT == 3 )); then
      _values 'schedule action' add list show pause resume run delete
      return
    fi
    _arguments "${config[@]}" '--file[Schedule JSON file]:schedule file:_files' '--interactive[Guide unresolved schedule-add values in a real terminal]' '*:schedule ID:'
    ;;
  runtime)
    if (( CURRENT == 3 )); then
      _values 'runtime action' install
      return
    fi
    _arguments "${config[@]}" '1:runtime:(llama.cpp)'
    ;;
  mcp)
    if (( CURRENT == 3 )); then
      _values 'MCP action' serve
      return
    fi
    _arguments "${config[@]}"
    ;;
  integrate)
    if (( CURRENT == 3 )); then
	  _values 'integration target' openai litellm mcp relay ui
      return
    fi
    case "$words[3]" in
      openai) _arguments "${config[@]}" '--json[Print redacted machine-readable connection settings]' '--show-token[Explicitly include the local API token in terminal output]' '--write-env[Create a new private environment file without overwriting]:environment file:_files' '--check[Verify service, authentication and route without inference]' '--live[Also send one explicit bounded live inference smoke request]' ;;
      litellm) _arguments "${config[@]}" '--json[Print redacted machine-readable integration settings]' '--write-config[Create a new secret-free LiteLLM configuration]:configuration file:_files' '--write-env[Create a new private LiteLLM environment file]:environment file:_files' ;;
      mcp) _arguments "${config[@]}" '--json[Print the MCP client configuration as JSON]' ;;
      relay) _arguments "${config[@]}" '--json[Print redacted credential metadata]' '--write-env[Create a new private producer environment file]:environment file:_files' '--subject[Remote application identity]:identity:' '--groups[Comma-separated scheduling groups]:groups:' '--lifetime-hours[Credential lifetime; 0 never expires]:hours:' '--max-queued-jobs[Producer queued-job limit]:count:' '--max-jobs-per-hour[Durable hourly admission limit]:count:' '--max-priority[Maximum producer-selected job priority]:priority:(0 20 40 60 80 100)' '--providers[Comma-separated provider allowlist]:providers:' '--allowed-tenants[Comma-separated authenticated tenant_id allowlist]:tenants:' '--egress[Producer egress ceiling]:egress:(local_only)' '--require-e2ee[Reject cleartext jobs for this producer]' '--scheduled-actions-policy[Strict JSON scheduled-action policy]:policy file:_files' ;;
      ui) _arguments "${config[@]}" '--json[Print redacted credential metadata]' '--write-env[Create a new private observer environment file]:environment file:_files' '--subject[Read-only UI identity]:identity:' '--lifetime-hours[Credential lifetime; 0 never expires]:hours:' '--allowed-subjects[Comma-separated visible producer subjects]:subjects:' '--allowed-tenants[Comma-separated visible tenant IDs]:tenants:' ;;
      *) _arguments '*:argument:' ;;
    esac
    ;;
  adapter)
    if (( CURRENT == 3 )); then
	  _values 'adapter action' list details doctor setup conformance enable disable start stop
      return
    fi
    case "$words[3]" in
	  setup) _arguments "${config[@]}" '--label[Human-readable profile label]:label:' '--driver[Bounded adapter driver ID]:driver:' '--route[Local route name]:route:' '--task[Handled job task]:task:' '--model[Exact model selector]:model:' '--timeout-seconds[Route timeout]:seconds:' '--principal[Scoped principal ID]:principal:' '--token-file[Private credential file]:credential file:_files' '--classification[Execution boundary]:classification:(local remote)' '*--option[Non-secret profile option as KEY=JSON]:option:' '--create-token[Create a missing credential without overwriting]' '--json[Print redacted machine-readable setup details]' '1:profile ID:' ;;
	  conformance) _arguments '--adapter[Exact adapter executable]:adapter executable:_files' '*--arg[One literal adapter argument]:argument:' '--profile[Safe adapter profile ID]:profile:' '--profile-file[Bounded JSON adapter profile]:profile file:_files' '--job-file[Side-effect-free bounded JSON job]:job file:_files' '--working-directory[Adapter working directory]:directory:_directories' '--timeout-seconds[Maximum seconds per isolated scenario]:seconds:' '--json[Print machine-readable conformance report]' ;;
	  list|doctor) _arguments "${config[@]}" '--account[Named cluster account]:account:' '--token[Scoped relay credential]:token:' '--token-file[Scoped credential file]:token file:_files' '--json[Print machine-readable JSON]' ;;
	  details|enable|disable|start|stop) _arguments "${config[@]}" '--account[Named cluster account]:account:' '--token[Scoped relay credential]:token:' '--token-file[Scoped credential file]:token file:_files' '--json[Print machine-readable JSON]' '1:adapter UID:' ;;
      *) _arguments '*:argument:' ;;
    esac
    ;;
  benchmark)
    _arguments '--json[Print machine-readable JSON]' '--samples[Timed samples per operation and concurrency]:count:' '--warmup[Warm-up samples per operation]:count:' '--database-jobs[Jobs used for database growth measurement]:count:' '--idle-duration[Idle relay sampling duration]:duration:' '--binary[Binary whose size is reported]:binary:_files'
    ;;
  verification)
    if (( CURRENT == 3 )); then
      _values 'verification action' verify
      return
    fi
    _arguments '--file[Signed verification statement]:statement file:_files' '--trust-key[Trusted issuer public key]:trust key:_files' '--artifact[Subject artifact to re-hash]:artifact file:_files' '--require-artifact[Require and re-hash the subject artifact]' '--evidence-dir[Directory containing signed evidence files]:directory:_directories' '--require-evidence[Require and re-hash every signed evidence file]' '--json[Print machine-readable verification result]'
    ;;
  cluster)
    if (( CURRENT == 3 )); then
	  _values 'cluster action' status events estimate node protocol conformance submit chat scheduled-action agent selftest route contract receipt login account token pairing pool configure dashboard pipeline lan
      return
    fi
    case "$words[3]" in
      status) _arguments "${config[@]}" '--json[Print machine-readable JSON]' ;;
	  events) _arguments "${config[@]}" '--token[Producer, observer, or admin token]:token:' '--after[Resume after this event sequence]:sequence:' '--limit[Events per page, 1-500]:count:' '--json[Print versioned event pages as JSON]' '--follow[Poll until a terminal event is observed]' '--pipeline[Read a pipeline-run stream]' '--poll[Follow polling interval]:duration:' '1:job or run ID:' ;;
	  estimate) _arguments "${config[@]}" '--token[Producer, observer, or admin token]:token:' '--json[Print versioned historical estimate as JSON]' '1:job ID:' ;;
	  node)
		if (( CURRENT == 4 )); then
		  _values 'node action' drain resume
		  return
		fi
		_arguments "${config[@]}" '--token[Relay admin token]:token:' '--json[Print machine-readable node state]' '*:node ID:'
		;;
	  protocol) _arguments "${config[@]}" '--token[Producer, observer, or admin token]:token:' '--json[Print machine-readable protocol manifest]' ;;
	  conformance)
		if (( CURRENT == 4 )); then
		  _values 'conformance target' relay worker resilience
		  return
		fi
		_arguments "${config[@]}" '--token[Producer, observer, or admin token]:token:' '--node[Exact worker ID or unique worker name]:worker:' '--json[Print machine-readable conformance report]'
		;;
      submit) _arguments "${config[@]}" '--account[Named cluster account]:account:' '--file[Cluster job JSON]:job file:_files' '--token[Producer token]:token:' '--wait[Wait for a final result]' '--e2ee[Encrypt payload]' '--stream[Stream adapter text]' '--artifacts[Artifact output directory]:directory:_directories' '--idempotency-key[Deduplicate an exact retry]:key:' '--interactive[Guide unresolved submission values in a real terminal]' ;;
	  chat) _arguments "${config[@]}" '--account[Named cluster account]:account:' '--token[Producer token]:token:' '--provider[Generation provider]:provider:(adapter ollama nuextract jina)' '--group[Worker group]:group:' '--model[Specific model]:model:' '--profile[Operator-configured adapter profile]:profile:' '--reasoning[Reasoning level]:level:(instant medium high xhigh pro max)' '--e2ee[Encrypt prompts and results]' '--session[Stable session ID]:session:' '--prompt[Send one turn and exit]:prompt:' '--artifacts[Artifact directory or auto/off]:directory:_directories' '--min-artifacts[Required verified files]:count:' '--image[Require a returned image]' '--min-images[Required verified images]:count:' '--attach-image[Attach a local image]:image file:_files' '--new-session[Open a fresh adapter session]' '--new-session-per-job[Open a fresh adapter session for every turn]' '--foreground-new-session[Ask the adapter to foreground a fresh session]' ;;
	  scheduled-action)
		if (( CURRENT == 4 )); then
		  _values 'scheduled action' preview confirm list show cancel
		  return
		fi
		_arguments "${config[@]}" '--account[Named cluster account]:account:' '--token[Exact producer credential]:token:' '--token-file[Exact producer credential file]:token file:_files' '--file[Scheduled action request JSON]:request file:_files' '--status[Status filter]:status:(preview active completed failed unknown cancelled expired)' '--limit[Actions to return, 1-100]:count:' '--json[Print machine-readable JSON]' '*:scheduled action ID:'
		;;
      agent)
        if (( CURRENT == 4 )); then
          _values 'agent action' plan run auto
          return
        fi
        case "$words[4]" in
		  plan|auto) _arguments "${config[@]}" '--token[Producer token]:token:' '--goal[High-level goal]:goal:' '--goal-file[Goal text file]:goal file:_files' '--policy[Named configured project authority (auto only)]:policy:' '--planner-provider[Planner provider]:provider:' '--planner-profile[Planner adapter profile]:profile:' '--planner-model[Exact planner model]:model:' '--allow-providers[Approved step providers]:providers:' '--allow-adapter-profiles[Approved adapter profiles]:profiles:' '--max-steps[Maximum plan steps]:count:' '--step-timeout[Per-step seconds]:seconds:' '--max-runtime[Total seconds]:seconds:' '--planner-timeout[Planner seconds]:seconds:' '--out[New plan file]:plan file:_files' ;;
          run) _arguments "${config[@]}" '--token[Producer token]:token:' '--plan[Reviewed plan file]:plan file:_files' '--approve[Exact plan SHA-256]:digest:' ;;
          *) _arguments '*:argument:' ;;
        esac
        ;;
	  selftest) _arguments "${config[@]}" '--providers[Checks to run]:providers:' '--local-model[Specific local model]:model:' '--run[Run live checks]' '--dry-run[Readiness checks only]' '--image[Also verify one image]' '--image-profile[Adapter profile for image verification]:profile:' '--artifacts[Artifact directory]:directory:_directories' '--keep-artifacts[Keep temporary artifacts]' '--timeout[Capacity wait timeout]:duration:' '--job-timeout[Per-job timeout]:duration:' '--poll[Polling interval]:duration:' ;;
      route)
        if (( CURRENT == 4 )); then
          _values 'route action' explain
          return
        fi
		_arguments "${config[@]}" '--account[Named cluster account]:account:' '--file[Cluster job JSON for a non-executing preview]:job file:_files' '--job[Assigned job ID]:job ID:' '--token[Producer token]:token:' '--json[Print machine-readable routing evidence]'
        ;;
	  contract)
		if (( CURRENT == 4 )); then
		  _values 'contract action' validate
		  return
		fi
		_arguments "${config[@]}" '--file[Cluster job JSON]:job file:_files' '--token[Producer or admin token]:token:' '--json[Print machine-readable validation result]'
		;;
	  receipt)
		if (( CURRENT == 4 )); then
		  _values 'receipt action' show export verify keygen
		  return
		fi
		_arguments "${config[@]}" '--token[Producer, observer, or admin token]:token:' '--out[New receipt JSON file]:receipt file:_files' '--file[Receipt JSON file]:receipt file:_files' '--signing-key[Operator receipt-signing private key]:key file:_files' '--trust-key[Explicitly trusted receipt public key]:key file:_files' '--offline[Verify a signed receipt without relay access]' '--private-out[New private receipt-signing key]:key file:_files' '--public-out[New public receipt trust key]:key file:_files' '--key-id[Signer key identifier]:ID:' '--issuer[Signer issuer name]:name:' '*:job ID:'
		;;
      configure) _arguments "${config[@]}" '--mode[Cluster mode]:mode:(local client relay worker all)' '--relay-url[Public relay URL]:URL:' '--public-url[Public HTTPS relay URL]:URL:' '--name[Worker node name]:name:' '--listen[Relay listen address]:address:' '--interactive[Guide unresolved values in a real terminal]' ;;
	  dashboard) _arguments "${config[@]}" '--no-open[Print URL without opening a page viewer]' ;;
      pipeline)
        if (( CURRENT == 4 )); then
          _values 'pipeline action or option' activity
        fi
        if [[ "$words[4]" == "activity" ]]; then
          _arguments "${config[@]}" '--token[Producer, observer, or admin token]:token:' '--json[Print versioned activity projection]' '1:pipeline run ID:'
        else
          _arguments "${config[@]}" '--name[Pipeline name]:name:' '--file[Pipeline input JSON]:input file:_files'
        fi
        ;;
	  login) _arguments "${config[@]}" '--token-file[Producer token file]:token file:_files' '--account[Named cluster account]:account:' '--relay[Relay URL for the account]:URL:' '--pool-authority-file[Customer pool authority]:authority file:_files' '--activate[Make this account the default]' ;;
	  account) _arguments "${config[@]}" '--json[Print machine-readable account metadata]' '1:account action:(list use remove)' '2:account name:' ;;
	  token)
		case "$words[4]" in
		  list) _arguments "${config[@]}" '--token[Relay admin token]:token:' '--limit[Records per page]:count:' '--offset[Record offset]:count:' '--json[Print machine-readable credential metadata]' ;;
		  revoke) _arguments "${config[@]}" '--token[Relay admin token]:token:' '--json[Print revoked credential metadata]' '1:token ID:' ;;
		  create) _arguments "${config[@]}" '--role[Token role]:role:(admin producer node observer)' '--subject[Token label]:label:' '--groups[Comma-separated scheduling groups]:groups:' '--lifetime-hours[Credential lifetime; 0 never expires]:hours:' '--max-queued-jobs[Producer queued-job limit]:count:' '--max-jobs-per-hour[Durable hourly admission limit]:count:' '--max-priority[Maximum producer-selected job priority]:priority:(0 20 40 60 80 100)' '--providers[Comma-separated provider allowlist]:providers:' '--allowed-tenants[Producer or observer tenant allowlist]:tenants:' '--allowed-subjects[Observer owner_subject allowlist]:subjects:' '--egress[Producer egress ceiling]:egress:(local_only)' '--require-e2ee[Reject cleartext jobs for this producer]' '--scheduled-actions-policy[Strict JSON scheduled-action policy]:policy file:_files' ;;
		  *) _arguments "${config[@]}" '1:token action:(create list revoke)' '--role[Token role]:role:(admin producer node observer)' '--subject[Token label]:label:' '--groups[Comma-separated scheduling groups]:groups:' '--lifetime-hours[Credential lifetime; 0 never expires]:hours:' '--max-queued-jobs[Producer queued-job limit]:count:' '--max-jobs-per-hour[Durable hourly admission limit]:count:' '--max-priority[Maximum producer-selected job priority]:priority:(0 20 40 60 80 100)' '--providers[Comma-separated provider allowlist]:providers:' '--allowed-tenants[Producer or observer tenant allowlist]:tenants:' '--allowed-subjects[Observer owner_subject allowlist]:subjects:' '--egress[Producer egress ceiling]:egress:(local_only)' '--require-e2ee[Reject cleartext jobs for this producer]' '--scheduled-actions-policy[Strict JSON scheduled-action policy]:policy file:_files' ;;
		esac
		;;
	  pairing) _arguments "${config[@]}" '--approve[Approve pairing code]:code:' '--deny[Deny pairing code]:code:' ;;
	  pool) _arguments '1:pool action:(init)' '--id[Stable customer pool ID]:ID:' '--out[New owner-only authority file]:file:_files' ;;
	  lan)
		if (( CURRENT == 4 )); then
		  _values 'LAN action' init relocate join status
		  return
		fi
		case "$words[4]" in
		  init) _arguments "${config[@]}" '--listen[Private or wildcard LAN address]:address:' '--advertise-host[Reachable LAN IP or local DNS name]:host:' '--out[New join bundle]:file:_files' ;;
		  relocate) _arguments "${config[@]}" '--listen[New private or wildcard LAN address]:address:' '--advertise-host[New LAN IP or local DNS name]:host:' '--certificate-out[New same-key certificate]:file:_files' '--out[New relocation bundle]:file:_files' ;;
		  join) _arguments "${config[@]}" '--bundle[Trusted LAN join bundle]:file:_files' '--name[Worker node name]:name:' ;;
		  status) _arguments "${config[@]}" ;;
		esac
		;;
      *) _arguments '*:argument:' ;;
    esac
    ;;
  route)
    if (( CURRENT == 3 )); then
      _values 'route action' explain
      return
    fi
    _arguments "${config[@]}" '--file[Cluster job JSON for a non-executing preview]:job file:_files' '--job[Assigned job ID]:job ID:' '--token[Producer token]:token:' '--json[Print machine-readable routing evidence]'
    ;;
  update)
    if (( CURRENT == 3 )); then
      _values 'update action' status check apply enable disable auto
      return
    fi
    _arguments "${config[@]}" '--force[Allow replacing a development build]' '--json[Print machine-readable JSON]' '--managed-service[Systemd service to restart]:service:' '--relay-only[Require only the relay to be idle]'
    ;;
  completion)
    if (( CURRENT == 3 )); then
      _values 'shell' powershell bash zsh
      return
    fi
    ;;
	selftest) _arguments "${config[@]}" '--providers[Checks to run]:providers:' '--local-model[Specific local model]:model:' '--run[Run live checks]' '--dry-run[Readiness checks only]' '--image[Also verify one image]' '--image-profile[Adapter profile for image verification]:profile:' '--artifacts[Artifact directory]:directory:_directories' '--keep-artifacts[Keep temporary artifacts]' '--timeout[Capacity wait timeout]:duration:' '--job-timeout[Per-job timeout]:duration:' '--poll[Polling interval]:duration:' ;;
  console) _arguments "${config[@]}" '--token[Scoped producer token for bounded work actions]:token:' ;;
  init|serve|health|guide|pull|relay) _arguments "${config[@]}" '*:argument:' ;;
  run) _arguments "${config[@]}" '--slots[Session worker job limit]:slots:' '--topmost[Keep the Windows console above other windows]' ;;
  stop) _arguments "${config[@]}" '--force[Stop even while jobs are active]' ;;
  uninstall) _arguments "${config[@]}" '--install-dir[Installation directory when automatic discovery is unavailable]:directory:_directories' '--purge[Also remove locally managed configuration and data]' '--force[Allow shutdown with active jobs or unreadable configuration]' '--yes[Confirm the displayed plan non-interactively]' '--dry-run[Show the exact plan without removing anything]' ;;
  submit) _arguments "${config[@]}" '--file[Job JSON file]:job file:_files' '--artifacts[Artifact output directory]:directory:_directories' ;;
  result) _arguments "${config[@]}" '1:job ID:' ;;
  review) _arguments "${config[@]}" '--job-dir[InkWall job directory]:directory:_directories' ;;
	dashboard) _arguments "${config[@]}" '--no-open[Print URL without opening a page viewer]' ;;
  status|doctor|resources) _arguments "${config[@]}" '--json[Print machine-readable JSON]' ;;
  hardware) _arguments '--json[Print machine-readable JSON]' ;;
  models) _arguments "${config[@]}" '--json[Print machine-readable JSON]' '--discover[Discover local model runtimes and files]' ;;
  pair) _arguments "${config[@]}" '--relay[Public relay URL]:URL:' '--identity[Identity file]:identity file:_files' '--name[Node name]:name:' '--interactive[Guide unresolved pairing values in a real terminal]' ;;
  worker) _arguments "${config[@]}" '--relay[Relay URL]:URL:' '--identity[Identity file]:identity file:_files' '--name[Worker display name]:name:' '--slots[Worker job limit]:slots:' '--providers[Allowed providers]:providers:' '--models[Allowed models]:models:' '--tasks[Allowed tasks]:tasks:' '--groups[Scheduling groups]:groups:' '--no-updates[Disable the worker updater]' '--topmost[Keep the Windows console above other windows]' ;;
  version|help) ;;
  *) _arguments '*:argument:_files' ;;
esac
`
}
