package main

import (
	"errors"
	"fmt"
	"strings"
)

var completionRootCommands = []string{
	"init", "serve", "run", "stop", "console", "submit", "schedule", "result", "review",
	"health", "dashboard", "status", "doctor", "hardware", "models", "resources", "uninstall",
	"pull", "runtime", "mcp", "integrate", "benchmark", "verification", "relay", "pair", "worker", "cluster", "route", "selftest", "update", "completion", "version", "help",
}

var completionSubcommands = map[string][]string{
	"schedule":     {"add", "list", "show", "pause", "resume", "run", "delete"},
	"runtime":      {"install"},
	"mcp":          {"serve"},
	"integrate":    {"openai", "mcp", "relay"},
	"verification": {"verify"},
	"cluster":      {"status", "events", "node", "protocol", "conformance", "submit", "chat", "agent", "selftest", "route", "contract", "receipt", "login", "token", "pairing", "configure", "dashboard", "pipeline"},
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
    integrate = @('openai','mcp','relay')
    verification = @('verify')
	cluster = @('status','events','node','protocol','conformance','submit','chat','agent','selftest','route','contract','receipt','login','token','pairing','configure','dashboard','pipeline')
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
    'console' = @('--config')
    'submit' = @('--file','--artifacts','--config')
    'schedule' = @('--file','--config')
    'result' = @('--config')
    'review' = @('--job-dir','--config')
    'health' = @('--config')
    'dashboard' = @('--config','--no-open')
    'status' = @('--config','--json')
    'doctor' = @('--config','--json')
    'hardware' = @('--json')
    'models' = @('--config','--json','--discover')
    'resources' = @('--config','--json')
    'pull' = @('--config')
    'runtime install' = @('--config')
    'mcp serve' = @('--config')
    'integrate openai' = @('--config','--json','--show-token','--write-env','--check','--live')
    'integrate mcp' = @('--config','--json')
    'integrate relay' = @('--config','--json','--write-env','--subject','--groups','--lifetime-hours')
    'benchmark' = @('--json','--samples','--warmup','--database-jobs','--idle-duration','--binary')
    'verification verify' = @('--file','--trust-key','--artifact','--require-artifact','--evidence-dir','--require-evidence','--json')
    'relay' = @('--config')
    'pair' = @('--config','--relay','--identity','--name')
    'worker' = @('--config','--relay','--identity','--name','--slots','--providers','--models','--tasks','--groups','--no-updates','--topmost')
    'cluster status' = @('--config','--json')
	'cluster events' = @('--config','--token','--after','--limit','--json','--follow','--pipeline','--poll')
	'cluster node' = @('drain','resume','--config','--token','--json')
	'cluster protocol' = @('--config','--token','--json')
	'cluster conformance' = @('relay','worker','--config','--token','--node','--json')
    'cluster submit' = @('--config','--file','--token','--wait','--e2ee','--stream','--artifacts','--idempotency-key')
    'cluster chat' = @('--config','--token','--provider','--group','--model','--profile','--reasoning','--e2ee','--session','--prompt','--artifacts','--min-artifacts','--image','--min-images','--attach-image','--new-session','--new-session-per-job','--foreground-new-session','--egress','--max-cost-usd')
    'cluster agent' = @('plan','run','auto','--config','--token','--goal','--goal-file','--policy','--planner-provider','--planner-profile','--planner-model','--allow-providers','--allow-adapter-profiles','--max-steps','--step-timeout','--max-runtime','--planner-timeout','--out','--plan','--approve')
	'cluster selftest' = @('--config','--providers','--local-model','--run','--dry-run','--image','--image-profile','--artifacts','--keep-artifacts','--timeout','--job-timeout','--poll')
    'cluster route' = @('explain','--config','--file','--job','--token','--json')
	'cluster contract' = @('validate','--config','--file','--token','--json')
	'cluster receipt' = @('show','export','verify','keygen','--config','--token','--out','--file','--signing-key','--trust-key','--offline','--private-out','--public-out','--key-id','--issuer')
    'route explain' = @('--config','--file','--job','--token','--json')
	'selftest' = @('--config','--providers','--local-model','--run','--dry-run','--image','--image-profile','--artifacts','--keep-artifacts','--timeout','--job-timeout','--poll')
    'cluster configure' = @('--config','--mode','--relay-url','--public-url','--name','--listen')
    'cluster dashboard' = @('--config','--no-open')
    'cluster pipeline' = @('--config','--name','--file')
    'cluster login' = @('--config','--token-file')
    'cluster token' = @('--config','--role','--subject')
    'cluster pairing' = @('--config','--approve','--deny')
    'update' = @('--config','--force','--json','--managed-service','--relay-only')
}
$script:ContextBridgeValueOptions = @{
    '--provider' = @('adapter','ollama','nuextract','jina')

    '--reasoning' = @('instant','medium','high','xhigh','pro','max')
    '--mode' = @('local','relay','worker','all')
    '--role' = @('producer','observer')
}
$script:ContextBridgeTakesValue = @(
	'--config','--install-dir','--file','--job','--artifacts','--artifact','--attach-image','--identity','--job-dir','--token-file','--trust-key','--evidence-dir','--write-env',
    '--slots','--endpoint','--relay','--name','--providers','--models','--tasks','--groups',
    '--token','--provider','--group','--model','--profile','--reasoning','--session','--prompt',
	'--min-artifacts','--min-images','--local-model','--image-profile','--timeout','--job-timeout','--poll','--after','--limit','--idempotency-key','--subject','--groups','--lifetime-hours',
    '--mode','--relay-url','--public-url','--listen','--role','--subject','--approve','--deny',
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
      schedule|runtime|mcp|integrate|verification|cluster|route|update)
        if [[ -n "$subcommand" && "$subcommand" != -* ]]; then
          option_key="$command $subcommand"
        fi
        ;;
    esac
  fi

  case "$previous" in
    --config|--file|--artifacts|--artifact|--attach-image|--identity|--job-dir|--token-file|--binary|--trust-key|--evidence-dir|--write-env)
      if declare -F _filedir >/dev/null 2>&1; then _filedir; else COMPREPLY=( $(compgen -f -- "$current") ); fi
      return ;;
    --provider) candidates="adapter ollama nuextract jina" ;;

    --reasoning) candidates="instant medium high xhigh pro max" ;;
    --mode) candidates="local relay worker all" ;;
    --role) candidates="producer observer" ;;
    --wait|--e2ee|--stream|--json|--follow|--pipeline|--show-token|--check|--live|--no-open|--topmost|--image|--new-session|--new-session-per-job|--foreground-new-session|--run|--dry-run|--keep-artifacts|--no-updates|--discover|--force|--relay-only|--require-artifact|--require-evidence) boolean_previous=1 ;;
  esac
  if [ -n "${candidates:-}" ]; then
    COMPREPLY=( $(compgen -W "$candidates" -- "$current") )
    return
  fi

  if [[ "$current" == -* || ( "$boolean_previous" -eq 1 && -z "$current" ) ]]; then
    candidates=""
    case "$option_key" in
      "cluster chat") candidates="--config --token --provider --group --model --profile --reasoning --e2ee --session --prompt --artifacts --min-artifacts --image --min-images --attach-image --new-session --new-session-per-job --foreground-new-session --egress --max-cost-usd" ;;
      "cluster agent") candidates="plan run auto --config --token --goal --goal-file --policy --planner-provider --planner-profile --planner-model --allow-providers --allow-adapter-profiles --max-steps --step-timeout --max-runtime --planner-timeout --out --plan --approve" ;;
	  "cluster selftest") candidates="--config --providers --local-model --run --dry-run --image --image-profile --artifacts --keep-artifacts --timeout --job-timeout --poll" ;;
      "cluster route") candidates="--config --file --job --token --json" ;;
	  "cluster contract") candidates="validate --config --file --token --json" ;;
	  "cluster receipt") candidates="show export verify keygen --config --token --out --file --signing-key --trust-key --offline --private-out --public-out --key-id --issuer" ;;
      "route explain") candidates="--config --file --job --token --json" ;;
      "cluster submit") candidates="--config --file --token --wait --e2ee --stream --artifacts --idempotency-key" ;;
      "cluster status") candidates="--config --json" ;;
	  "cluster events") candidates="--config --token --after --limit --json --follow --pipeline --poll" ;;
	  "cluster node") candidates="drain resume --config --token --json" ;;
	  "cluster protocol") candidates="--config --token --json" ;;
	  "cluster conformance") candidates="relay worker --config --token --node --json" ;;
      "cluster configure") candidates="--config --mode --relay-url --public-url --name --listen" ;;
      "cluster dashboard") candidates="--config --no-open" ;;
      "cluster pipeline") candidates="--config --name --file" ;;
      "cluster login") candidates="--config --token-file" ;;
      "cluster token") candidates="--config --role --subject" ;;
      "cluster pairing") candidates="--config --approve --deny" ;;
      "mcp serve") candidates="--config" ;;
      "integrate openai") candidates="--config --json --show-token --write-env --check --live" ;;
      "integrate mcp") candidates="--config --json" ;;
      "integrate relay") candidates="--config --json --write-env --subject --groups --lifetime-hours" ;;
      "verification verify") candidates="--file --trust-key --artifact --require-artifact --evidence-dir --require-evidence --json" ;;
      benchmark) candidates="--json --samples --warmup --database-jobs --idle-duration --binary" ;;
      "schedule add") candidates="--config --file" ;;
      "schedule "*) candidates="--config --file" ;;
      init|serve|console|result|health|pull|relay) candidates="--config" ;;
      run) candidates="--config --slots --topmost" ;;
      stop) candidates="--config --force" ;;
      uninstall) candidates="--config --install-dir --purge --force --yes --dry-run" ;;
      submit) candidates="--config --file --artifacts" ;;
      review) candidates="--config --job-dir" ;;
      dashboard) candidates="--config --no-open" ;;
      pair) candidates="--config --relay --identity --name" ;;
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
	  candidates="relay worker"
  elif [[ "$option_key" == "cluster agent" && "$COMP_CWORD" -eq 3 ]]; then
    candidates="plan run auto"
	elif [[ "$option_key" == "cluster receipt" && "$COMP_CWORD" -eq 3 ]]; then
	  candidates="show export verify keygen"
  elif [ "$COMP_CWORD" -eq 1 ]; then
    candidates="init serve run stop uninstall console submit schedule result review health dashboard status doctor hardware models resources pull runtime mcp integrate benchmark verification relay pair worker cluster route selftest update completion version help"
  elif [ "$COMP_CWORD" -eq 2 ]; then
    case "$command" in
      schedule) candidates="add list show pause resume run delete" ;;
      runtime) candidates="install" ;;
      mcp) candidates="serve" ;;
      integrate) candidates="openai mcp relay" ;;
      verification) candidates="verify" ;;
	  cluster) candidates="status events node protocol conformance submit chat agent selftest route contract receipt login token pairing configure dashboard pipeline" ;;
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
    'hardware:Show detected hardware'
    'models:Show model inventory'
    'resources:Show detected portable resource packs'
    'pull:Download a managed model'
    'runtime:Manage local runtimes'
    'mcp:Expose bounded local tools over MCP stdio'
    'integrate:Generate safe application connection settings'
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
    _arguments "${config[@]}" '--file[Schedule JSON file]:schedule file:_files' '*:schedule ID:'
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
      _values 'integration target' openai mcp relay
      return
    fi
    case "$words[3]" in
      openai) _arguments "${config[@]}" '--json[Print redacted machine-readable connection settings]' '--show-token[Explicitly include the local API token in terminal output]' '--write-env[Create a new private environment file without overwriting]:environment file:_files' '--check[Verify service, authentication and route without inference]' '--live[Also send one explicit bounded live inference smoke request]' ;;
      mcp) _arguments "${config[@]}" '--json[Print the MCP client configuration as JSON]' ;;
      relay) _arguments "${config[@]}" '--json[Print redacted credential metadata]' '--write-env[Create a new private producer environment file]:environment file:_files' '--subject[Remote application identity]:identity:' '--groups[Comma-separated scheduling groups]:groups:' '--lifetime-hours[Credential lifetime; 0 never expires]:hours:' ;;
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
	  _values 'cluster action' status events node protocol conformance submit chat agent selftest route contract receipt login token pairing configure dashboard pipeline
      return
    fi
    case "$words[3]" in
      status) _arguments "${config[@]}" '--json[Print machine-readable JSON]' ;;
	  events) _arguments "${config[@]}" '--token[Producer, observer, or admin token]:token:' '--after[Resume after this event sequence]:sequence:' '--limit[Events per page, 1-500]:count:' '--json[Print versioned event pages as JSON]' '--follow[Poll until a terminal event is observed]' '--pipeline[Read a pipeline-run stream]' '--poll[Follow polling interval]:duration:' '1:job or run ID:' ;;
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
		  _values 'conformance target' relay worker
		  return
		fi
		_arguments "${config[@]}" '--token[Producer, observer, or admin token]:token:' '--node[Exact worker ID or unique worker name]:worker:' '--json[Print machine-readable conformance report]'
		;;
      submit) _arguments "${config[@]}" '--file[Cluster job JSON]:job file:_files' '--token[Producer token]:token:' '--wait[Wait for a final result]' '--e2ee[Encrypt payload]' '--stream[Stream adapter text]' '--artifacts[Artifact output directory]:directory:_directories' '--idempotency-key[Deduplicate an exact retry]:key:' ;;
	  chat) _arguments "${config[@]}" '--token[Producer token]:token:' '--provider[Generation provider]:provider:(adapter ollama nuextract jina)' '--group[Worker group]:group:' '--model[Specific model]:model:' '--profile[Operator-configured adapter profile]:profile:' '--reasoning[Reasoning level]:level:(instant medium high xhigh pro max)' '--e2ee[Encrypt prompts and results]' '--session[Stable session ID]:session:' '--prompt[Send one turn and exit]:prompt:' '--artifacts[Artifact directory or auto/off]:directory:_directories' '--min-artifacts[Required verified files]:count:' '--image[Require a returned image]' '--min-images[Required verified images]:count:' '--attach-image[Attach a local image]:image file:_files' '--new-session[Open a fresh adapter session]' '--new-session-per-job[Open a fresh adapter session for every turn]' '--foreground-new-session[Ask the adapter to foreground a fresh session]' ;;
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
        _arguments "${config[@]}" '--file[Cluster job JSON for a non-executing preview]:job file:_files' '--job[Assigned job ID]:job ID:' '--token[Producer token]:token:' '--json[Print machine-readable routing evidence]'
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
      configure) _arguments "${config[@]}" '--mode[Cluster mode]:mode:(local relay worker all)' '--relay-url[Public relay URL]:URL:' '--public-url[Public HTTPS relay URL]:URL:' '--name[Worker node name]:name:' '--listen[Relay listen address]:address:' ;;
	  dashboard) _arguments "${config[@]}" '--no-open[Print URL without opening a page viewer]' ;;
      pipeline) _arguments "${config[@]}" '--name[Pipeline name]:name:' '--file[Pipeline input JSON]:input file:_files' ;;
      login) _arguments "${config[@]}" '--token-file[Producer token file]:token file:_files' ;;
      token) _arguments "${config[@]}" '--role[Token role]:role:(producer observer)' '--subject[Token label]:label:' ;;
      pairing) _arguments "${config[@]}" '--approve[Approve pairing code]:code:' '--deny[Deny pairing code]:code:' ;;
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
  init|serve|console|health|pull|relay) _arguments "${config[@]}" '*:argument:' ;;
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
  pair) _arguments "${config[@]}" '--relay[Public relay URL]:URL:' '--identity[Identity file]:identity file:_files' '--name[Node name]:name:' ;;
  worker) _arguments "${config[@]}" '--relay[Relay URL]:URL:' '--identity[Identity file]:identity file:_files' '--name[Worker display name]:name:' '--slots[Worker job limit]:slots:' '--providers[Allowed providers]:providers:' '--models[Allowed models]:models:' '--tasks[Allowed tasks]:tasks:' '--groups[Scheduling groups]:groups:' '--no-updates[Disable the worker updater]' '--topmost[Keep the Windows console above other windows]' ;;
  version|help) ;;
  *) _arguments '*:argument:_files' ;;
esac
`
}
