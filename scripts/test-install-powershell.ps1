$ErrorActionPreference = 'Stop'

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw $Message }
}

$repository = Split-Path -Parent $PSScriptRoot
$installer = Join-Path $repository 'install.ps1'
$tokens = $null
$parseErrors = $null
$ast = [Management.Automation.Language.Parser]::ParseFile($installer, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -ne 0) {
    throw "install.ps1 failed to parse: $($parseErrors[0].Message)"
}
$profileFunctionAst = @($ast.FindAll({
    param($node)
    $node -is [Management.Automation.Language.FunctionDefinitionAst] -and
        $node.Name -eq 'Update-ContextBridgeCompletionProfile'
}, $true))
Assert-True ($profileFunctionAst.Count -eq 1) 'Expected exactly one completion-profile helper in install.ps1.'
Invoke-Expression $profileFunctionAst[0].Extent.Text
$commandsFunctionAst = @($ast.FindAll({
    param($node)
    $node -is [Management.Automation.Language.FunctionDefinitionAst] -and
        $node.Name -eq 'Get-ContextBridgeCompletionCommands'
}, $true))
Assert-True ($commandsFunctionAst.Count -eq 1) 'Expected exactly one completion-command helper in install.ps1.'
Invoke-Expression $commandsFunctionAst[0].Extent.Text
$manifestFunctionAst = @($ast.FindAll({
    param($node)
    $node -is [Management.Automation.Language.FunctionDefinitionAst] -and
        $node.Name -eq 'Write-ContextBridgeInstallManifest'
}, $true))
Assert-True ($manifestFunctionAst.Count -eq 1) 'Expected exactly one ownership-manifest helper in install.ps1.'
Invoke-Expression $manifestFunctionAst[0].Extent.Text
Assert-True ((Get-ContextBridgeCompletionCommands -Commands @('contextbridge', 'cb')) -eq 'contextbridge and cb') 'Installed cb alias was omitted from the completion success label.'
Assert-True ((Get-ContextBridgeCompletionCommands -Commands @('contextbridge')) -eq 'contextbridge') 'Single-command completion label changed.'
Assert-True ((Get-ContextBridgeCompletionCommands -Commands @('contextbridge', 'cb', 'bridge-ai')) -eq 'contextbridge, cb and bridge-ai') 'Custom command was omitted from the completion success label.'
$nameFunctionAst = @($ast.FindAll({
    param($node)
    $node -is [Management.Automation.Language.FunctionDefinitionAst] -and
        $node.Name -eq 'Test-ContextBridgeCommandName'
}, $true))
Assert-True ($nameFunctionAst.Count -eq 1) 'Expected exactly one custom command-name validator in install.ps1.'
Invoke-Expression $nameFunctionAst[0].Extent.Text
Assert-True (Test-ContextBridgeCommandName 'bridge-ai') 'A safe custom command name was rejected.'
Assert-True (-not (Test-ContextBridgeCommandName '../cb')) 'A path-like custom command name was accepted.'
Assert-True (-not (Test-ContextBridgeCommandName '9bridge')) 'A custom command starting with a digit was accepted.'
$modeFunctionAst = @($ast.FindAll({
    param($node)
    $node -is [Management.Automation.Language.FunctionDefinitionAst] -and
        $node.Name -eq 'Resolve-ContextBridgeInstallMode'
}, $true))
Assert-True ($modeFunctionAst.Count -eq 1) 'Expected exactly one installer mode resolver in install.ps1.'
Invoke-Expression $modeFunctionAst[0].Extent.Text
Assert-True ((Resolve-ContextBridgeInstallMode -Mode ask -Relay 'https://relay.example.test' -PublicRelay '' -Interactive $true) -eq 'worker') 'A supplied worker relay URL did not infer worker mode.'
Assert-True ((Resolve-ContextBridgeInstallMode -Mode ask -Relay '' -PublicRelay 'https://relay.example.test' -Interactive $true) -eq 'relay') 'A supplied public relay URL did not infer relay mode.'
Assert-True ((Resolve-ContextBridgeInstallMode -Mode sender -Relay 'https://relay.example.test' -PublicRelay '' -Interactive $false) -eq 'client') 'Sender mode was not normalized to client mode.'
Assert-True ((Resolve-ContextBridgeInstallMode -Mode ask -Relay '' -PublicRelay '' -Interactive $false) -eq 'local') 'A headless install without cluster hints did not remain local.'
$ambiguousModeFailed = $false
try {
    Resolve-ContextBridgeInstallMode -Mode ask -Relay 'https://worker.example.test' -PublicRelay 'https://relay.example.test' -Interactive $false | Out-Null
} catch {
    $ambiguousModeFailed = $true
}
Assert-True $ambiguousModeFailed 'Conflicting relay-role hints were silently accepted.'
$pathFunctionAst = @($ast.FindAll({
    param($node)
    $node -is [Management.Automation.Language.FunctionDefinitionAst] -and
        $node.Name -eq 'Test-ContextBridgeCommandPath'
}, $true))
Assert-True ($pathFunctionAst.Count -eq 1) 'Expected exactly one cb command-path helper in install.ps1.'
Invoke-Expression $pathFunctionAst[0].Extent.Text
$expectedAlias = Join-Path ([IO.Path]::GetTempPath()) 'contextbridge-owner\cb.cmd'
Assert-True (Test-ContextBridgeCommandPath -CommandInfo ([pscustomobject]@{ Path = $expectedAlias }) -ExpectedPath $expectedAlias) 'The managed cb launcher was not recognized as the active command.'
Assert-True (-not (Test-ContextBridgeCommandPath -CommandInfo ([pscustomobject]@{ Path = (Join-Path ([IO.Path]::GetTempPath()) 'foreign\cb.exe') }) -ExpectedPath $expectedAlias)) 'A foreign cb executable was accepted as the managed launcher.'
Assert-True (-not (Test-ContextBridgeCommandPath -CommandInfo ([pscustomobject]@{ Source = 'user-alias' }) -ExpectedPath $expectedAlias)) 'A PowerShell alias/function was accepted as the managed native launcher.'

$testRoot = Join-Path ([IO.Path]::GetTempPath()) ('contextbridge-installer-profile-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $testRoot | Out-Null
try {
    $utf8 = New-Object Text.UTF8Encoding($false)
    $beginMarker = '# >>> ContextBridge completion >>>'
    $endMarker = '# <<< ContextBridge completion <<<'
    $completionPath = Join-Path $testRoot "completion's script.ps1"

    $manifestRoot = Join-Path $testRoot 'manifest-root'
    New-Item -ItemType Directory -Path $manifestRoot | Out-Null
    Write-ContextBridgeInstallManifest -Root $manifestRoot -OwnedPaths @('contextbridge.exe', 'config.example.yml', 'optional-adapter')
    $manifest = Get-Content -LiteralPath (Join-Path $manifestRoot '.contextbridge-install.json') -Raw | ConvertFrom-Json
    Assert-True ($manifest.schema_version -eq 1) 'Ownership manifest schema changed.'
    Assert-True ($manifest.product -eq 'ContextBridge') 'Ownership manifest product identity changed.'
    Assert-True (@($manifest.paths).Contains('optional-adapter')) 'Optional package path was omitted from the ownership manifest.'
    $manifestHash = (Get-FileHash -LiteralPath (Join-Path $manifestRoot '.contextbridge-install.json') -Algorithm SHA256).Hash
    $unsafeManifestFailed = $false
    try {
        Write-ContextBridgeInstallManifest -Root $manifestRoot -OwnedPaths @('contextbridge.exe', '../outside')
    } catch {
        $unsafeManifestFailed = $true
    }
    Assert-True $unsafeManifestFailed 'Ownership manifest accepted a path escape.'
    Assert-True ((Get-FileHash -LiteralPath (Join-Path $manifestRoot '.contextbridge-install.json') -Algorithm SHA256).Hash -eq $manifestHash) 'Rejected manifest input changed the last valid manifest.'

    # A CRLF profile written by the installer must be byte-identical after a
    # second run and retain exactly one registration block.
    $profile = Join-Path $testRoot 'idempotent-profile.ps1'
    [IO.File]::WriteAllText($profile, "user-before`r`n", $utf8)
    Update-ContextBridgeCompletionProfile -ProfilePath $profile -CompletionPath $completionPath
    $firstHash = (Get-FileHash -LiteralPath $profile -Algorithm SHA256).Hash
    Update-ContextBridgeCompletionProfile -ProfilePath $profile -CompletionPath $completionPath
    $secondHash = (Get-FileHash -LiteralPath $profile -Algorithm SHA256).Hash
    $content = [IO.File]::ReadAllText($profile)
    Assert-True ($firstHash -eq $secondHash) 'A second installer run changed an already current CRLF profile.'
    Assert-True (([regex]::Matches($content, [regex]::Escape($beginMarker))).Count -eq 1) 'The begin marker was duplicated.'
    Assert-True (([regex]::Matches($content, [regex]::Escape($endMarker))).Count -eq 1) 'The end marker was duplicated.'
    Assert-True ($content.Contains(". '$($completionPath.Replace("'", "''"))'")) 'The completion path was not safely single-quote escaped.'

    # One valid LF block is replaceable while surrounding user content and its
    # line-ending convention remain intact.
    $validProfile = Join-Path $testRoot 'valid-profile.ps1'
    [IO.File]::WriteAllText($validProfile, "user-before`n$beginMarker`n. 'old.ps1'`n$endMarker`nuser-after`n", $utf8)
    Update-ContextBridgeCompletionProfile -ProfilePath $validProfile -CompletionPath $completionPath
    $validContent = [IO.File]::ReadAllText($validProfile)
    Assert-True ($validContent.Contains("user-before`n")) 'Content before a valid managed block was changed.'
    Assert-True ($validContent.Contains("`nuser-after`n")) 'Content after a valid managed block was changed.'
    Assert-True (-not $validContent.Contains('old.ps1')) 'The old valid managed block was not replaced.'
    Assert-True (-not $validContent.Contains("`r`n")) 'An LF-only profile was converted to mixed line endings.'

    $malformedCases = @{
        reversed = "$endMarker`r`nuser-before=true`r`n$beginMarker`r`nuser-after=true`r`n"
        duplicate = "$beginMarker`r`nmanaged=true`r`n$endMarker`r`nuser-middle=true`r`n$beginMarker`r`nuser-after=true`r`n"
        unclosed = "user-before=true`r`n$beginMarker`r`nuser-after=true`r`n"
        loneEnd = "user-before=true`r`n$endMarker`r`nuser-after=true`r`n"
    }
    foreach ($case in $malformedCases.GetEnumerator()) {
        $malformedProfile = Join-Path $testRoot ("malformed-{0}.ps1" -f $case.Key)
        [IO.File]::WriteAllText($malformedProfile, $case.Value, $utf8)
        $beforeHash = (Get-FileHash -LiteralPath $malformedProfile -Algorithm SHA256).Hash
        $failed = $false
        try {
            Update-ContextBridgeCompletionProfile -ProfilePath $malformedProfile -CompletionPath $completionPath
        } catch {
            $failed = $true
        }
        $afterHash = (Get-FileHash -LiteralPath $malformedProfile -Algorithm SHA256).Hash
        Assert-True $failed "Malformed case $($case.Key) was accepted."
        Assert-True ($beforeHash -eq $afterHash) "Malformed case $($case.Key) changed the profile."
    }

    Write-Host 'PowerShell installer completion profile verified.'
} finally {
    $resolvedRoot = [IO.Path]::GetFullPath($testRoot)
    $expectedRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath()).TrimEnd([IO.Path]::DirectorySeparatorChar) + [IO.Path]::DirectorySeparatorChar
    if ($resolvedRoot.StartsWith($expectedRoot, [StringComparison]::OrdinalIgnoreCase) -and
        [IO.Path]::GetFileName($resolvedRoot) -match '^contextbridge-installer-profile-[0-9a-f]{32}$') {
        Remove-Item -LiteralPath $resolvedRoot -Recurse -Force
    }
}
