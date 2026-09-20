function Write-ContextBridgeChecksumFile {
    param(
        [Parameter(Mandatory = $true)][string]$Directory,
        [Parameter(Mandatory = $true)][string]$Path
    )

    $checksumLines = Get-ChildItem -LiteralPath $Directory -File |
        Where-Object { $_.FullName -ne [IO.Path]::GetFullPath($Path) } |
        Sort-Object Name |
        ForEach-Object {
            "{0}  {1}" -f (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant(), $_.Name
        }
    if ($checksumLines.Count -eq 0) {
        throw "Cannot write a checksum manifest for an empty release directory: $Directory"
    }

    # SHA256SUMS is consumed by POSIX tools and install.sh. Write explicit LF
    # delimiters even when the release is assembled on Windows; CRLF would make
    # sha256sum treat the carriage return as part of each asset name.
    $utf8NoBom = New-Object Text.UTF8Encoding($false)
    [IO.File]::WriteAllText($Path, (($checksumLines -join "`n") + "`n"), $utf8NoBom)
}
