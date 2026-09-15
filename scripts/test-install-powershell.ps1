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
Assert-True ((Get-ContextBridgeCompletionCommands -AliasInstalled $true) -eq 'contextbridge and cb') 'Installed cb alias was omitted from the completion success label.'
Assert-True ((Get-ContextBridgeCompletionCommands -AliasInstalled $false) -eq 'contextbridge') 'Skipped cb alias was falsely claimed by the completion success label.'
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
