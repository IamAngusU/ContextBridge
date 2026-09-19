package main

import (
	"errors"
	"fmt"
	"strings"
)

var completionRootCommands = []string{
	"init", "serve", "run", "stop", "console", "submit", "schedule", "result", "review",
	"health", "dashboard", "status", "browser", "doctor", "hardware", "models", "resources",
	"pull", "runtime", "mcp", "benchmark", "relay", "pair", "worker", "cluster", "route", "selftest", "update", "completion", "version", "help",
}

var completionSubcommands = map[string][]string{
	"schedule":   {"add", "list", "show", "pause", "resume", "run", "delete"},
	"browser":    {"inspect"},
	"runtime":    {"install"},
	"mcp":        {"serve"},
	"cluster":    {"status", "submit", "chat", "agent", "selftest", "route", "login", "token", "pairing", "configure", "dashboard", "pipeline"},
	"route":      {"explain"},
	"update":     {"status", "check", "apply", "enable", "disable", "auto"},
	"completion": {"powershell", "bash", "zsh"},
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
    browser = @('inspect')
    runtime = @('install')
    mcp = @('serve')
    cluster = @('status','submit','chat','agent','selftest','route','login','token','pairing','configure','dashboard','pipeline')
    route = @('explain')
    update = @('status','check','apply','enable','disable','auto')
    completion = @('powershell','bash','zsh')
}
$script:ContextBridgeOptions = @{
    'init' = @('--config')
    'serve' = @('--config')
    'run' = @('--config','--slots','--topmost')
    'stop' = @('--config','--force')
    'console' = @('--config')
    'submit' = @('--file','--artifacts','--config')
    'schedule' = @('--file','--config')
    'result' = @('--config')
    'review' = @('--job-dir','--config')
    'health' = @('--config')
    'dashboard' = @('--config','--no-open')
    'status' = @('--config','--json')
    'browser inspect' = @('--config','--tab')
    'doctor' = @('--config','--json')
    'hardware' = @('--json')
    'models' = @('--config','--json','--discover')
    'resources' = @('--config','--json')
    'pull' = @('--config')
    'runtime install' = @('--config')
    'mcp serve' = @('--config')
    'benchmark' = @('--json','--samples','--warmup','--database-jobs','--idle-duration','--binary','--extension-root')
    'relay' = @('--config')
    'pair' = @('--config','--relay','--identity','--name')
    'worker' = @('--config','--relay','--identity','--name','--slots','--providers','--models','--tasks','--groups','--no-updates','--topmost')
    'cluster status' = @('--config','--json')
    'cluster submit' = @('--config','--file','--token','--wait','--e2ee','--stream','--artifacts','--idempotency-key')
    'cluster chat' = @('--config','--token','--provider','--group','--model','--profile','--reasoning','--e2ee','--session','--prompt','--artifacts','--min-artifacts','--image','--min-images','--music','--attach-image','--new-chat','--new-chat-per-job','--foreground-new-chat')
    'cluster agent' = @('plan','run','--config','--token','--goal','--goal-file','--planner-provider','--planner-profile','--planner-model','--allow-providers','--allow-browser-profiles','--max-steps','--step-timeout','--max-runtime','--planner-timeout','--out','--plan','--approve')
    'cluster selftest' = @('--config','--providers','--local-model','--run','--dry-run','--image','--artifacts','--keep-artifacts','--timeout','--job-timeout','--poll')
    'cluster route' = @('explain','--config','--file','--job','--token','--json')
    'route explain' = @('--config','--file','--job','--token','--json')
    'selftest' = @('--config','--providers','--local-model','--run','--dry-run','--image','--artifacts','--keep-artifacts','--timeout','--job-timeout','--poll')
    'cluster configure' = @('--config','--mode','--relay-url','--public-url','--name','--listen')
    'cluster dashboard' = @('--config','--no-open')
    'cluster pipeline' = @('--config','--name','--file')
    'cluster login' = @('--config','--token-file')
    'cluster token' = @('--config','--role','--subject')
    'cluster pairing' = @('--config','--approve','--deny')
    'update' = @('--config','--force','--json','--managed-service','--relay-only')
}
$script:ContextBridgeValueOptions = @{
    '--provider' = @('browser','ollama','nuextract','jina')
    '--profile' = @('chatgpt','gemini')
    '--reasoning' = @('instant','medium','high','xhigh','pro','max')
    '--mode' = @('local','relay','worker','all')
    '--role' = @('producer','observer')
}
$script:ContextBridgeTakesValue = @(
    '--config','--file','--job','--artifacts','--attach-image','--identity','--job-dir','--token-file',
    '--slots','--tab','--relay','--name','--providers','--models','--tasks','--groups',
    '--token','--provider','--group','--model','--profile','--reasoning','--session','--prompt',
    '--min-artifacts','--min-images','--local-model','--timeout','--job-timeout','--poll','--idempotency-key',
    '--mode','--relay-url','--public-url','--listen','--role','--subject','--approve','--deny',
    '--managed-service','--samples','--warmup','--database-jobs','--idle-duration','--binary','--extension-root',
    '--goal','--goal-file','--planner-provider','--planner-profile','--planner-model','--allow-providers',
    '--allow-browser-profiles','--max-steps','--step-timeout','--max-runtime','--planner-timeout','--out','--plan','--approve'
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
    # (--r followed by Tab) and partial nested actions work as well as trailing spaces.
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
      schedule|browser|runtime|mcp|cluster|route|update)
        if [[ -n "$subcommand" && "$subcommand" != -* ]]; then
          option_key="$command $subcommand"
        fi
        ;;
    esac
  fi

  case "$previous" in
    --config|--file|--artifacts|--attach-image|--identity|--job-dir|--token-file|--binary)
      if declare -F _filedir >/dev/null 2>&1; then _filedir; else COMPREPLY=( $(compgen -f -- "$current") ); fi
      return ;;
    --extension-root)
      if declare -F _filedir >/dev/null 2>&1; then _filedir -d; else COMPREPLY=( $(compgen -d -- "$current") ); fi
      return ;;
    --provider) candidates="browser ollama nuextract jina" ;;
    --profile) candidates="chatgpt gemini" ;;
    --reasoning) candidates="instant medium high xhigh pro max" ;;
    --mode) candidates="local relay worker all" ;;
    --role) candidates="producer observer" ;;
    --wait|--e2ee|--stream|--json|--no-open|--topmost|--image|--music|--new-chat|--new-chat-per-job|--foreground-new-chat|--run|--dry-run|--keep-artifacts|--no-updates|--discover|--force|--relay-only) boolean_previous=1 ;;
  esac
  if [ -n "${candidates:-}" ]; then
    COMPREPLY=( $(compgen -W "$candidates" -- "$current") )
    return
  fi

  if [[ "$current" == -* || ( "$boolean_previous" -eq 1 && -z "$current" ) ]]; then
    candidates=""
    case "$option_key" in
      "cluster chat") candidates="--config --token --provider --group --model --profile --reasoning --e2ee --session --prompt --artifacts --min-artifacts --image --min-images --music --attach-image --new-chat --new-chat-per-job --foreground-new-chat" ;;
      "cluster agent") candidates="plan run --config --token --goal --goal-file --planner-provider --planner-profile --planner-model --allow-providers --allow-browser-profiles --max-steps --step-timeout --max-runtime --planner-timeout --out --plan --approve" ;;
      "cluster selftest") candidates="--config --providers --local-model --run --dry-run --image --artifacts --keep-artifacts --timeout --job-timeout --poll" ;;
      "cluster route") candidates="--config --file --job --token --json" ;;
      "route explain") candidates="--config --file --job --token --json" ;;
      "cluster submit") candidates="--config --file --token --wait --e2ee --stream --artifacts --idempotency-key" ;;
      "cluster status") candidates="--config --json" ;;
      "cluster configure") candidates="--config --mode --relay-url --public-url --name --listen" ;;
      "cluster dashboard") candidates="--config --no-open" ;;
      "cluster pipeline") candidates="--config --name --file" ;;
      "cluster login") candidates="--config --token-file" ;;
      "cluster token") candidates="--config --role --subject" ;;
      "cluster pairing") candidates="--config --approve --deny" ;;
      "browser inspect") candidates="--config --tab" ;;
      "mcp serve") candidates="--config" ;;
      benchmark) candidates="--json --samples --warmup --database-jobs --idle-duration --binary --extension-root" ;;
      "schedule add") candidates="--config --file" ;;
      "schedule "*) candidates="--config --file" ;;
      init|serve|console|result|health|pull|relay) candidates="--config" ;;
      run) candidates="--config --slots --topmost" ;;
      stop) candidates="--config --force" ;;
      submit) candidates="--config --file --artifacts" ;;
      review) candidates="--config --job-dir" ;;
      dashboard) candidates="--config --no-open" ;;
      pair) candidates="--config --relay --identity --name" ;;
      worker) candidates="--config --relay --identity --name --slots --providers --models --tasks --groups --no-updates --topmost" ;;
      status|doctor|resources) candidates="--config --json" ;;
      selftest) candidates="--config --providers --local-model --run --dry-run --image --artifacts --keep-artifacts --timeout --job-timeout --poll" ;;
      models) candidates="--config --json --discover" ;;
      hardware) candidates="--json" ;;
      update|"update "*) candidates="--config --force --json --managed-service --relay-only" ;;
    esac
  elif [[ "$option_key" == "cluster route" && "$COMP_CWORD" -eq 3 ]]; then
    candidates="explain"
  elif [[ "$option_key" == "cluster agent" && "$COMP_CWORD" -eq 3 ]]; then
    candidates="plan run"
  elif [ "$COMP_CWORD" -eq 1 ]; then
    candidates="init serve run stop console submit schedule result review health dashboard status browser doctor hardware models resources pull runtime mcp benchmark relay pair worker cluster route selftest update completion version help"
  elif [ "$COMP_CWORD" -eq 2 ]; then
    case "$command" in
      schedule) candidates="add list show pause resume run delete" ;;
      browser) candidates="inspect" ;;
      runtime) candidates="install" ;;
      mcp) candidates="serve" ;;
      cluster) candidates="status submit chat agent selftest route login token pairing configure dashboard pipeline" ;;
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
    'console:Attach a live terminal to the running service'
    'submit:Submit a local JSON job'
    'schedule:Manage durable schedules'
    'result:Read a saved job result'
    'review:Review a saved decision'
    'health:Read local health'
    'dashboard:Open the local dashboard'
    'status:Show local status'
    'browser:Inspect attached browser tabs'
    'doctor:Check setup and connectivity'
    'hardware:Show detected hardware'
    'models:Show model inventory'
    'resources:Show detected portable resource packs'
    'pull:Download a managed model'
    'runtime:Manage local runtimes'
    'mcp:Expose bounded local tools over MCP stdio'
    'benchmark:Measure bridge-only overhead and resource footprint'
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
  browser)
    if (( CURRENT == 3 )); then
      _values 'browser action' inspect
      return
    fi
    _arguments "${config[@]}" '--tab[Browser tab ID]:tab ID:'
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
  benchmark)
    _arguments '--json[Print machine-readable JSON]' '--samples[Timed samples per operation and concurrency]:count:' '--warmup[Warm-up samples per operation]:count:' '--database-jobs[Jobs used for database growth measurement]:count:' '--idle-duration[Idle relay sampling duration]:duration:' '--binary[Binary whose size is reported]:binary:_files' '--extension-root[Extension source root]:directory:_directories'
    ;;
  cluster)
    if (( CURRENT == 3 )); then
      _values 'cluster action' status submit chat agent selftest route login token pairing configure dashboard pipeline
      return
    fi
    case "$words[3]" in
      status) _arguments "${config[@]}" '--json[Print machine-readable JSON]' ;;
      submit) _arguments "${config[@]}" '--file[Cluster job JSON]:job file:_files' '--token[Producer token]:token:' '--wait[Wait for a final result]' '--e2ee[Encrypt payload]' '--stream[Stream browser text]' '--artifacts[Artifact output directory]:directory:_directories' '--idempotency-key[Deduplicate an exact retry]:key:' ;;
      chat) _arguments "${config[@]}" '--token[Producer token]:token:' '--provider[Generation provider]:provider:(browser ollama nuextract jina)' '--group[Worker group]:group:' '--model[Specific model]:model:' '--profile[Browser profile]:profile:(chatgpt gemini)' '--reasoning[Reasoning level]:level:(instant medium high xhigh pro max)' '--e2ee[Encrypt prompts and results]' '--session[Stable conversation ID]:session:' '--prompt[Send one turn and exit]:prompt:' '--artifacts[Artifact directory or auto/off]:directory:_directories' '--min-artifacts[Required verified files]:count:' '--image[Require a returned image]' '--min-images[Required verified images]:count:' '--music[Require verified music output]' '--attach-image[Attach a local image]:image file:_files' '--new-chat[Open a fresh chat for the session]' '--new-chat-per-job[Open a fresh chat for every turn]' '--foreground-new-chat[Show a newly opened chat]' ;;
      agent)
        if (( CURRENT == 4 )); then
          _values 'agent action' plan run
          return
        fi
        case "$words[4]" in
          plan) _arguments "${config[@]}" '--token[Producer token]:token:' '--goal[High-level goal]:goal:' '--goal-file[Goal text file]:goal file:_files' '--planner-provider[Planner provider]:provider:' '--planner-profile[Planner browser profile]:profile:(chatgpt gemini)' '--planner-model[Exact planner model]:model:' '--allow-providers[Approved step providers]:providers:' '--allow-browser-profiles[Approved browser profiles]:profiles:' '--max-steps[Maximum plan steps]:count:' '--step-timeout[Per-step seconds]:seconds:' '--max-runtime[Total seconds]:seconds:' '--planner-timeout[Planner seconds]:seconds:' '--out[New plan file]:plan file:_files' ;;
          run) _arguments "${config[@]}" '--token[Producer token]:token:' '--plan[Reviewed plan file]:plan file:_files' '--approve[Exact plan SHA-256]:digest:' ;;
          *) _arguments '*:argument:' ;;
        esac
        ;;
      selftest) _arguments "${config[@]}" '--providers[Checks to run]:providers:' '--local-model[Specific local model]:model:' '--run[Run live checks]' '--dry-run[Readiness checks only]' '--image[Also verify one image]' '--artifacts[Artifact directory]:directory:_directories' '--keep-artifacts[Keep temporary artifacts]' '--timeout[Capacity wait timeout]:duration:' '--job-timeout[Per-job timeout]:duration:' '--poll[Polling interval]:duration:' ;;
      route)
        if (( CURRENT == 4 )); then
          _values 'route action' explain
          return
        fi
        _arguments "${config[@]}" '--file[Cluster job JSON for a non-executing preview]:job file:_files' '--job[Assigned job ID]:job ID:' '--token[Producer token]:token:' '--json[Print machine-readable routing evidence]'
        ;;
      configure) _arguments "${config[@]}" '--mode[Cluster mode]:mode:(local relay worker all)' '--relay-url[Public relay URL]:URL:' '--public-url[Public HTTPS relay URL]:URL:' '--name[Worker node name]:name:' '--listen[Relay listen address]:address:' ;;
      dashboard) _arguments "${config[@]}" '--no-open[Print URL without opening a browser]' ;;
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
  selftest) _arguments "${config[@]}" '--providers[Checks to run]:providers:' '--local-model[Specific local model]:model:' '--run[Run live checks]' '--dry-run[Readiness checks only]' '--image[Also verify one image]' '--artifacts[Artifact directory]:directory:_directories' '--keep-artifacts[Keep temporary artifacts]' '--timeout[Capacity wait timeout]:duration:' '--job-timeout[Per-job timeout]:duration:' '--poll[Polling interval]:duration:' ;;
  init|serve|console|health|pull|relay) _arguments "${config[@]}" '*:argument:' ;;
  run) _arguments "${config[@]}" '--slots[Session worker job limit]:slots:' '--topmost[Keep the Windows console above other windows]' ;;
  stop) _arguments "${config[@]}" '--force[Stop even while jobs are active]' ;;
  submit) _arguments "${config[@]}" '--file[Job JSON file]:job file:_files' '--artifacts[Artifact output directory]:directory:_directories' ;;
  result) _arguments "${config[@]}" '1:job ID:' ;;
  review) _arguments "${config[@]}" '--job-dir[InkWall job directory]:directory:_directories' ;;
  dashboard) _arguments "${config[@]}" '--no-open[Print URL without opening a browser]' ;;
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
