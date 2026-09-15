<?php
declare(strict_types=1);

const CONTEXTBRIDGE_REPOSITORY = 'IamAngusU/ContextBridge';
const CONTEXTBRIDGE_BASE = '/contextbridge';
const CONTEXTBRIDGE_METRIC_RETENTION_DAYS = 31;
const CONTEXTBRIDGE_METRIC_REQUESTS_PER_CLIENT_DAY = 8;
const CONTEXTBRIDGE_METRIC_UNIQUES_PER_KIND_DAY = 10000;

if (!defined('CONTEXTBRIDGE_LIBRARY_ONLY')) {
    $requestPath = parse_url((string) ($_SERVER['REQUEST_URI'] ?? CONTEXTBRIDGE_BASE . '/'), PHP_URL_PATH) ?: CONTEXTBRIDGE_BASE . '/';

    if ($requestPath === CONTEXTBRIDGE_BASE . '/api/stats') {
        respondWithStats();
    }
    if ($requestPath === CONTEXTBRIDGE_BASE . '/install.sh') {
        respondWithInstaller('install.sh', 'text/x-shellscript; charset=utf-8');
    }
    if ($requestPath === CONTEXTBRIDGE_BASE . '/install.ps1') {
        respondWithInstaller('install.ps1', 'text/plain; charset=utf-8');
    }
    if ($requestPath === CONTEXTBRIDGE_BASE . '/favicon.svg') {
        respondWithFile(dirname(__DIR__) . '/extension/assets/contextbridge-mark.svg', 'image/svg+xml; charset=utf-8', true);
    }
}

if (!defined('CONTEXTBRIDGE_LIBRARY_ONLY')) {
    header('Content-Type: text/html; charset=utf-8');
    header("Content-Security-Policy: default-src 'self'; base-uri 'none'; connect-src 'self'; font-src 'self'; form-action 'self'; frame-ancestors 'none'; img-src 'self' data:; object-src 'none'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'");
    header('Referrer-Policy: strict-origin-when-cross-origin');
    header('X-Content-Type-Options: nosniff');
    header('X-Frame-Options: DENY');
}

function respondWithStats(): never
{
    $github = githubStatistics();
    $downloads = localDownloadStatistics();
    jsonResponse([
        'repository' => CONTEXTBRIDGE_REPOSITORY,
        'stars' => $github['stars'],
        'release_downloads' => $github['release_downloads'],
        'installer_requests' => $downloads['total'],
        'unique_installers' => $downloads['unique'],
        'unique_window' => 'current UTC day',
        'updated_at' => $github['updated_at'],
    ]);
}

function respondWithInstaller(string $name, string $contentType): never
{
    if (($_SERVER['REQUEST_METHOD'] ?? 'GET') === 'GET') {
        recordDownload($name);
    }
    respondWithFile(dirname(__DIR__) . '/' . $name, $contentType, false);
}

function respondWithFile(string $path, string $contentType, bool $cacheable): never
{
    if (!is_file($path) || !is_readable($path)) {
        http_response_code(404);
        header('Content-Type: text/plain; charset=utf-8');
        echo "Not found\n";
        exit;
    }
    header('Content-Type: ' . $contentType);
    header('X-Content-Type-Options: nosniff');
    header($cacheable ? 'Cache-Control: public, max-age=86400' : 'Cache-Control: no-store');
    if (($_SERVER['REQUEST_METHOD'] ?? 'GET') !== 'HEAD') {
        readfile($path);
    }
    exit;
}

function githubStatistics(): array
{
    $directory = siteDataDirectory();
    $cachePath = $directory . '/github-stats.json';
    $cached = readJsonFile($cachePath);
    if (isset($cached['fetched_at']) && (time() - (int) $cached['fetched_at']) < 300) {
        return normalizeGithubStatistics($cached);
    }

    $repository = githubRequest('https://api.github.com/repos/' . CONTEXTBRIDGE_REPOSITORY);
    $releases = githubRequest('https://api.github.com/repos/' . CONTEXTBRIDGE_REPOSITORY . '/releases?per_page=100');
    if (is_array($repository) && is_array($releases)) {
        $releaseDownloads = 0;
        foreach ($releases as $release) {
            if (!is_array($release) || !isset($release['assets']) || !is_array($release['assets'])) {
                continue;
            }
            foreach ($release['assets'] as $asset) {
                $name = (string) ($asset['name'] ?? '');
                if ($name === 'SHA256SUMS') {
                    continue;
                }
                $releaseDownloads += max(0, (int) ($asset['download_count'] ?? 0));
            }
        }
        $fresh = [
            'stars' => max(0, (int) ($repository['stargazers_count'] ?? 0)),
            'release_downloads' => $releaseDownloads,
            'updated_at' => gmdate(DATE_ATOM),
            'fetched_at' => time(),
        ];
        writeJsonFile($cachePath, $fresh);
        return $fresh;
    }

    return normalizeGithubStatistics($cached);
}

function normalizeGithubStatistics(array $value): array
{
    return [
        'stars' => max(0, (int) ($value['stars'] ?? 0)),
        'release_downloads' => max(0, (int) ($value['release_downloads'] ?? 0)),
        'updated_at' => (string) ($value['updated_at'] ?? gmdate(DATE_ATOM)),
    ];
}

function githubRequest(string $url): ?array
{
    $handle = curl_init($url);
    if ($handle === false) {
        return null;
    }
    curl_setopt_array($handle, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_CONNECTTIMEOUT => 3,
        CURLOPT_TIMEOUT => 7,
        CURLOPT_FOLLOWLOCATION => true,
        CURLOPT_MAXREDIRS => 3,
        CURLOPT_HTTPHEADER => ['Accept: application/vnd.github+json', 'User-Agent: ContextBridge-Website'],
    ]);
    $body = curl_exec($handle);
    $status = (int) curl_getinfo($handle, CURLINFO_RESPONSE_CODE);
    curl_close($handle);
    if (!is_string($body) || $status !== 200 || strlen($body) > 8 * 1024 * 1024) {
        return null;
    }
    $decoded = json_decode($body, true);
    return is_array($decoded) ? $decoded : null;
}

function recordDownload(string $kind, ?int $now = null): void
{
    if (!in_array($kind, ['install.sh', 'install.ps1'], true)) {
        return;
    }
    $now ??= time();
    $bucket = gmdate('Y-m-d', $now);
    $cutoff = gmdate('Y-m-d', $now - ((CONTEXTBRIDGE_METRIC_RETENTION_DAYS - 1) * 86400));
    $transactionStarted = false;
    try {
        $database = siteDatabase();
        $fingerprint = clientFingerprint($bucket);
        $database->exec('BEGIN IMMEDIATE');
        $transactionStarted = true;

        $prune = $database->prepare('DELETE FROM download_unique_windows WHERE bucket < :cutoff');
        $prune->bindValue(':cutoff', $cutoff, SQLITE3_TEXT);
        $prune->execute();

        $lookup = $database->prepare('SELECT request_count FROM download_unique_windows WHERE kind = :kind AND bucket = :bucket AND fingerprint = :fingerprint');
        bindMetricIdentity($lookup, $kind, $bucket, $fingerprint);
        $existing = $lookup->execute()->fetchArray(SQLITE3_ASSOC);
        $count = is_array($existing) ? max(0, (int) ($existing['request_count'] ?? 0)) : -1;

        if ($count < 0) {
            $cardinality = $database->prepare('SELECT COUNT(*) FROM download_unique_windows WHERE kind = :kind AND bucket = :bucket');
            $cardinality->bindValue(':kind', $kind, SQLITE3_TEXT);
            $cardinality->bindValue(':bucket', $bucket, SQLITE3_TEXT);
            $uniqueCount = (int) $cardinality->execute()->fetchArray(SQLITE3_NUM)[0];
            if ($uniqueCount >= CONTEXTBRIDGE_METRIC_UNIQUES_PER_KIND_DAY) {
                $database->exec('COMMIT');
                $transactionStarted = false;
                $database->close();
                return;
            }
            $insert = $database->prepare('INSERT INTO download_unique_windows (kind, bucket, fingerprint, request_count, last_seen) VALUES (:kind, :bucket, :fingerprint, 1, :last_seen)');
            bindMetricIdentity($insert, $kind, $bucket, $fingerprint);
            $insert->bindValue(':last_seen', gmdate(DATE_ATOM, $now), SQLITE3_TEXT);
            $insert->execute();
            incrementDownloadTotal($database, $kind);
        } elseif ($count < CONTEXTBRIDGE_METRIC_REQUESTS_PER_CLIENT_DAY) {
            $update = $database->prepare('UPDATE download_unique_windows SET request_count = request_count + 1, last_seen = :last_seen WHERE kind = :kind AND bucket = :bucket AND fingerprint = :fingerprint AND request_count < :request_limit');
            bindMetricIdentity($update, $kind, $bucket, $fingerprint);
            $update->bindValue(':last_seen', gmdate(DATE_ATOM, $now), SQLITE3_TEXT);
            $update->bindValue(':request_limit', CONTEXTBRIDGE_METRIC_REQUESTS_PER_CLIENT_DAY, SQLITE3_INTEGER);
            $update->execute();
            if ($database->changes() === 1) {
                incrementDownloadTotal($database, $kind);
            }
        }
        $database->exec('COMMIT');
        $transactionStarted = false;
        $database->close();
    } catch (Throwable $error) {
        if (isset($database) && $database instanceof SQLite3) {
            if ($transactionStarted) {
                $database->exec('ROLLBACK');
            }
            $database->close();
        }
        error_log('ContextBridge download metric failed: ' . $error->getMessage());
    }
}

function bindMetricIdentity(SQLite3Stmt $statement, string $kind, string $bucket, string $fingerprint): void
{
    $statement->bindValue(':kind', $kind, SQLITE3_TEXT);
    $statement->bindValue(':bucket', $bucket, SQLITE3_TEXT);
    $statement->bindValue(':fingerprint', $fingerprint, SQLITE3_TEXT);
}

function incrementDownloadTotal(SQLite3 $database, string $kind): void
{
    $statement = $database->prepare('INSERT INTO download_totals (kind, total) VALUES (:kind, 1) ON CONFLICT(kind) DO UPDATE SET total = total + 1');
    $statement->bindValue(':kind', $kind, SQLITE3_TEXT);
    $statement->execute();
}

function localDownloadStatistics(?int $now = null): array
{
    $now ??= time();
    try {
        $database = siteDatabase();
        $total = (int) $database->querySingle('SELECT COALESCE(SUM(total), 0) FROM download_totals');
        $bucket = gmdate('Y-m-d', $now);
        $statement = $database->prepare('SELECT COUNT(DISTINCT fingerprint) FROM download_unique_windows WHERE bucket = :bucket');
        $statement->bindValue(':bucket', $bucket, SQLITE3_TEXT);
        $unique = (int) $statement->execute()->fetchArray(SQLITE3_NUM)[0];
        $database->close();
        return ['total' => max(0, $total), 'unique' => max(0, $unique)];
    } catch (Throwable $error) {
        error_log('ContextBridge stats read failed: ' . $error->getMessage());
        return ['total' => 0, 'unique' => 0];
    }
}

function siteDatabase(): SQLite3
{
    $path = siteDataDirectory() . '/downloads.sqlite';
    $database = new SQLite3($path, SQLITE3_OPEN_READWRITE | SQLITE3_OPEN_CREATE);
    @chmod($path, 0600);
    $database->busyTimeout(3000);
    $database->exec('PRAGMA journal_mode = WAL');
    $database->exec('PRAGMA synchronous = NORMAL');
    $database->exec('PRAGMA secure_delete = ON');
    $database->exec('CREATE TABLE IF NOT EXISTS download_totals (kind TEXT PRIMARY KEY, total INTEGER NOT NULL) WITHOUT ROWID');
    $database->exec('CREATE TABLE IF NOT EXISTS download_unique_windows (kind TEXT NOT NULL, bucket TEXT NOT NULL, fingerprint TEXT NOT NULL, request_count INTEGER NOT NULL CHECK(request_count BETWEEN 1 AND 8), last_seen TEXT NOT NULL, PRIMARY KEY (kind, bucket, fingerprint)) WITHOUT ROWID');
    $database->exec('CREATE INDEX IF NOT EXISTS download_unique_windows_bucket ON download_unique_windows (bucket)');
    // Legacy rows combined full user-agent strings into permanent variants.
    // They cannot be safely migrated into the rotating privacy model.
    $database->exec('DROP TABLE IF EXISTS download_uniques');
    return $database;
}

function clientFingerprint(?string $bucket = null): string
{
    $bucket ??= gmdate('Y-m-d');
    $address = maskedAddress((string) ($_SERVER['REMOTE_ADDR'] ?? 'unknown'));
    // User-Agent is intentionally excluded: it is attacker-controlled,
    // high-cardinality, and unnecessary for the coarse installer metric.
    return hash_hmac('sha256', "installer-network-v2\n" . $bucket . "\n" . $address, siteSecret());
}

function maskedAddress(string $address): string
{
    $binary = @inet_pton($address);
    if (!is_string($binary)) {
        return 'unknown';
    }
    $length = strlen($binary);
    if ($length === 4) {
        return bin2hex(substr($binary, 0, 3));
    }
    if ($length === 16) {
        return bin2hex(substr($binary, 0, 6));
    }
    return 'unknown';
}

function siteSecret(): string
{
    $path = siteDataDirectory() . '/fingerprint-secret';
    $handle = @fopen($path, 'c+b');
    if ($handle === false || !flock($handle, LOCK_EX)) {
        if (is_resource($handle)) {
            fclose($handle);
        }
        throw new RuntimeException('metric secret is unavailable');
    }
    try {
        rewind($handle);
        $existing = stream_get_contents($handle);
        if (is_string($existing) && strlen($existing) >= 32) {
            return $existing;
        }
        $secret = random_bytes(32);
        if (!ftruncate($handle, 0) || rewind($handle) === false || fwrite($handle, $secret) !== strlen($secret) || !fflush($handle)) {
            throw new RuntimeException('metric secret could not be saved');
        }
        @chmod($path, 0600);
        return $secret;
    } finally {
        flock($handle, LOCK_UN);
        fclose($handle);
    }
}

function siteDataDirectory(): string
{
    $configured = getenv('CONTEXTBRIDGE_SITE_DATA');
    $directory = is_string($configured) && $configured !== '' ? $configured : '/var/lib/contextbridge-site';
    if (!is_dir($directory) && !@mkdir($directory, 0700, true) && !is_dir($directory)) {
        throw new RuntimeException('site data directory is unavailable');
    }
    return $directory;
}

function readJsonFile(string $path): array
{
    $raw = @file_get_contents($path);
    if (!is_string($raw)) {
        return [];
    }
    $decoded = json_decode($raw, true);
    return is_array($decoded) ? $decoded : [];
}

function writeJsonFile(string $path, array $value): void
{
    $temporary = $path . '.' . bin2hex(random_bytes(6)) . '.tmp';
    file_put_contents($temporary, json_encode($value, JSON_UNESCAPED_SLASHES | JSON_PRETTY_PRINT) . "\n", LOCK_EX);
    @chmod($temporary, 0600);
    @rename($temporary, $path);
}

function jsonResponse(array $value): never
{
    header('Content-Type: application/json; charset=utf-8');
    header('Cache-Control: public, max-age=60, stale-while-revalidate=240');
    header('X-Content-Type-Options: nosniff');
    echo json_encode($value, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE) . "\n";
    exit;
}

if (defined('CONTEXTBRIDGE_LIBRARY_ONLY')) {
    return;
}
?>
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <meta name="color-scheme" content="light" />
  <meta name="description" content="ContextBridge connects private computers and local AI runtimes through one secure relay." />
  <title>ContextBridge | Route AI jobs across the compute you control</title>
  <link rel="icon" href="/contextbridge/favicon.svg" type="image/svg+xml" />
  <style>
    :root {
      --bg: #f4f4ef;
      --bg-top: #fbfbf7;
      --surface: rgba(255, 255, 251, .72);
      --surface-solid: #fffefa;
      --ink: #171815;
      --ink-soft: #343630;
      --muted: #777a72;
      --faint: #a7aaa2;
      --line: rgba(23, 24, 21, .10);
      --line-strong: rgba(23, 24, 21, .17);
      --accent: #ef6b2e;
      --accent-deep: #d95219;
      --accent-soft: #fff0e7;
      --success: #23815a;
      --success-soft: #e9f5ee;
      --shadow: 0 24px 80px rgba(18, 19, 16, .105);
      --ease-out: cubic-bezier(.16, 1, .3, 1);
      --ease-morph: cubic-bezier(.65, 0, .35, 1);
      --ease-spring: cubic-bezier(.2, 1.35, .34, 1);
      --sans: Inter, ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      --mono: "SFMono-Regular", "Cascadia Code", "Roboto Mono", Consolas, monospace;
      --content: min(1460px, calc(100vw - 72px));
    }

    * { box-sizing: border-box; }
    html { min-width: 320px; background: var(--bg); scroll-behavior: smooth; }
    body {
      margin: 0;
      min-height: 100vh;
      color: var(--ink);
      font-family: var(--sans);
      -webkit-font-smoothing: antialiased;
      background:
        radial-gradient(circle at 5% -16%, rgba(255,255,255,.98), transparent 34%),
        radial-gradient(circle at 93% 5%, rgba(239,107,46,.05), transparent 24%),
        linear-gradient(180deg, var(--bg-top), var(--bg) 76%);
      overflow-x: hidden;
    }

    button, input { font: inherit; }
    button, a { -webkit-tap-highlight-color: transparent; }
    button:focus-visible, a:focus-visible, input:focus-visible {
      outline: 3px solid rgba(239,107,46,.22);
      outline-offset: 3px;
    }
    ::selection { background: rgba(239,107,46,.18); }

    .noise {
      position: fixed;
      inset: 0;
      z-index: -1;
      pointer-events: none;
      opacity: .2;
      background-image: url("data:image/svg+xml,%3Csvg viewBox='0 0 180 180' xmlns='http://www.w3.org/2000/svg'%3E%3Cfilter id='n'%3E%3CfeTurbulence type='fractalNoise' baseFrequency='.88' numOctaves='3' stitchTiles='stitch'/%3E%3C/filter%3E%3Crect width='100%25' height='100%25' filter='url(%23n)' opacity='.035'/%3E%3C/svg%3E");
    }

    .page {
      min-height: 100vh;
      display: grid;
      grid-template-rows: auto 1fr auto;
    }

    .topbar {
      width: var(--content);
      height: 88px;
      margin: 0 auto;
      display: flex;
      align-items: center;
      justify-content: space-between;
      border-bottom: 1px solid var(--line);
      opacity: 0;
      transform: translateY(-12px);
      animation: reveal .78s .04s var(--ease-out) forwards;
    }

    .brand {
      display: inline-flex;
      align-items: center;
      gap: 12px;
      color: var(--ink);
      text-decoration: none;
      font-weight: 710;
      letter-spacing: -.026em;
    }

    .brand-mark {
      width: 36px;
      height: 36px;
      overflow: hidden;
      border-radius: 12px;
      corner-shape: squircle;
      box-shadow: inset 0 0 0 1px rgba(255,255,255,.12);
      transition: transform .42s var(--ease-spring), border-radius .42s var(--ease-morph);
    }
    .brand-mark svg { display: block; width: 100%; height: 100%; }
    .brand:hover .brand-mark { transform: rotate(-5deg) scale(1.045); border-radius: 15px 10px 14px 11px; }

    .github-link {
      min-height: 39px;
      display: inline-flex;
      align-items: center;
      gap: 9px;
      padding: 0 13px;
      color: var(--ink);
      text-decoration: none;
      font-size: 14px;
      font-weight: 650;
      border: 1px solid var(--line-strong);
      border-radius: 13px;
      corner-shape: squircle;
      background: rgba(255,255,251,.48);
      transition: transform .32s var(--ease-spring), background .25s ease, border-radius .35s var(--ease-morph), border-color .25s ease;
    }
    .github-link:hover { transform: translateY(-2px); background: var(--surface-solid); border-color: rgba(23,24,21,.24); border-radius: 16px 11px 15px 12px; }
    .github-link > svg { width: 16px; height: 16px; }
    .github-divider {
      width: 1px;
      height: 16px;
      margin-left: 2px;
      background: var(--line-strong);
    }
    .github-stars {
      min-width: 28px;
      display: inline-flex;
      align-items: center;
      justify-content: flex-end;
      gap: 5px;
      color: var(--ink-soft);
      font-variant-numeric: tabular-nums;
    }
    .github-stars svg { width: 13px; height: 13px; color: #c48720; }
    .github-stars[data-loading="true"] { color: var(--faint); }

    main {
      width: var(--content);
      margin: 0 auto;
      padding: clamp(62px, 7vw, 112px) 0 clamp(60px, 7vw, 100px);
      display: grid;
      grid-template-columns: minmax(440px, .84fr) minmax(620px, 1.16fr);
      align-items: center;
      gap: clamp(56px, 7vw, 116px);
    }

    .hero { max-width: 650px; }
    h1 {
      margin: 0;
      max-width: 690px;
      font-size: clamp(56px, 4.65vw, 80px);
      line-height: .975;
      letter-spacing: -.065em;
      font-weight: 735;
      text-wrap: balance;
      opacity: 0;
      transform: translateY(24px);
      animation: reveal .95s .2s var(--ease-out) forwards;
    }
    h1 .hero-line { display: block; }
    h1 .accent {
      color: var(--accent);
      position: relative;
      width: max-content;
      max-width: 100%;
      white-space: nowrap;
    }
    h1 .accent::after {
      content: "";
      position: absolute;
      left: 1%;
      right: 0;
      bottom: -.005em;
      height: .065em;
      border-radius: 999px;
      background: currentColor;
      opacity: .22;
      transform: scaleX(0);
      transform-origin: left;
      animation: lineIn .86s .88s var(--ease-out) forwards;
    }

    .subhead {
      max-width: 590px;
      margin: 26px 0 0;
      color: var(--muted);
      font-size: clamp(18px, 1.35vw, 21px);
      line-height: 1.58;
      letter-spacing: -.018em;
      opacity: 0;
      transform: translateY(18px);
      animation: reveal .84s .33s var(--ease-out) forwards;
    }

    .actions {
      margin-top: 34px;
      display: flex;
      align-items: center;
      gap: 22px;
      flex-wrap: wrap;
      opacity: 0;
      transform: translateY(16px);
      animation: reveal .84s .44s var(--ease-out) forwards;
    }

    .primary {
      position: relative;
      min-height: 52px;
      display: inline-flex;
      align-items: center;
      gap: 11px;
      padding: 0 20px;
      color: #fff;
      background: var(--ink);
      text-decoration: none;
      font-weight: 680;
      border-radius: 15px;
      corner-shape: squircle;
      overflow: hidden;
      box-shadow: 0 12px 28px rgba(20,21,18,.15);
      transition: transform .36s var(--ease-spring), border-radius .38s var(--ease-morph), box-shadow .3s ease;
    }
    .primary::before {
      content: "";
      position: absolute;
      inset: -50%;
      background: linear-gradient(105deg, transparent 35%, rgba(255,255,255,.18) 49%, transparent 63%);
      transform: translateX(-72%);
      transition: transform .72s var(--ease-out);
    }
    .primary:hover { transform: translateY(-3px); border-radius: 19px 12px 18px 13px; box-shadow: 0 17px 36px rgba(20,21,18,.19); }
    .primary:hover::before { transform: translateX(72%); }
    .primary span, .primary svg { position: relative; z-index: 1; }
    .primary svg { width: 17px; height: 17px; transition: transform .28s var(--ease-out); }
    .primary:hover svg { transform: translateX(3px); }

    .secondary {
      min-height: 46px;
      padding: 0;
      border: 0;
      background: transparent;
      color: var(--ink-soft);
      display: inline-flex;
      align-items: center;
      gap: 9px;
      cursor: pointer;
      font-weight: 620;
      border-bottom: 1px solid rgba(23,24,21,.25);
      transition: color .22s ease, border-color .22s ease, transform .3s var(--ease-spring);
    }
    .secondary:hover { color: var(--accent-deep); border-color: currentColor; transform: translateY(-2px); }
    .secondary svg { width: 17px; height: 17px; }

    .install-wrap {
      margin-top: 24px;
      max-width: 610px;
      opacity: 0;
      transform: translateY(14px);
      animation: reveal .82s .54s var(--ease-out) forwards;
    }

    .install-platforms {
      display: flex;
      flex-wrap: wrap;
      gap: 6px;
      margin: 0 0 8px 4px;
    }
    .install-platform {
      border: 0;
      border-radius: 999px;
      padding: 6px 10px;
      background: transparent;
      color: var(--muted);
      font: 700 11px/1 var(--sans);
      cursor: pointer;
      transition: color .2s ease, background .2s ease;
    }
    .install-platform[aria-pressed="true"] {
      background: var(--accent-soft);
      color: var(--accent-deep);
    }
    .install-platform:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }

    .install-bar {
      min-height: 58px;
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      align-items: center;
      border: 1px solid var(--line-strong);
      border-radius: 18px;
      corner-shape: squircle;
      background: rgba(255,255,251,.66);
      overflow: hidden;
      box-shadow: 0 10px 28px rgba(20,21,18,.045);
      transition: border-radius .4s var(--ease-morph), border-color .25s ease, background .25s ease, transform .35s var(--ease-spring);
    }
    .install-bar:hover { border-color: rgba(23,24,21,.24); background: var(--surface-solid); border-radius: 22px 15px 21px 16px; transform: translateY(-2px); }
    .install-code {
      min-width: 0;
      padding: 0 18px;
      overflow: auto hidden;
      scrollbar-width: none;
      white-space: nowrap;
      font-family: var(--mono);
      font-size: 13px;
      color: var(--ink-soft);
    }
    .install-code::-webkit-scrollbar { display: none; }
    .install-code .command { color: var(--accent-deep); font-weight: 740; }
    .install-code .url { color: var(--ink); }
    .copy-button {
      width: 56px;
      height: 56px;
      display: grid;
      place-items: center;
      border: 0;
      border-left: 1px solid var(--line);
      background: transparent;
      color: var(--muted);
      cursor: pointer;
      transition: color .2s ease, background .2s ease, transform .32s var(--ease-spring);
    }
    .copy-button:hover { color: var(--ink); background: rgba(23,24,21,.035); }
    .copy-button:active { transform: scale(.92); }
    .copy-button svg { width: 17px; height: 17px; }
    .install-meta {
      min-height: 20px;
      display: flex;
      align-items: center;
      gap: 14px;
      margin: 9px 4px 0;
      color: var(--muted);
      font-family: var(--mono);
      font-size: 10px;
      opacity: 0;
      transition: opacity .25s ease;
    }
    .install-meta.ready { opacity: 1; }
    .install-meta span + span::before { content: "·"; margin-right: 14px; color: var(--faint); }

    .relay-drawer {
      display: grid;
      grid-template-rows: 0fr;
      opacity: 0;
      transform: translateY(-8px);
      transition: grid-template-rows .55s var(--ease-out), opacity .35s ease, transform .55s var(--ease-out);
    }
    .relay-drawer.open { grid-template-rows: 1fr; opacity: 1; transform: translateY(0); }
    .relay-clip { overflow: hidden; }
    .relay-form {
      margin-top: 10px;
      min-height: 58px;
      display: grid;
      grid-template-columns: auto minmax(0, 1fr) auto;
      align-items: center;
      gap: 12px;
      padding: 7px 7px 7px 17px;
      border: 1px solid var(--line);
      border-radius: 18px;
      corner-shape: squircle;
      background: rgba(255,255,251,.48);
    }
    .relay-label {
      color: var(--faint);
      font-family: var(--mono);
      font-size: 10px;
      letter-spacing: .08em;
      text-transform: uppercase;
    }
    .relay-input {
      min-width: 0;
      height: 42px;
      border: 0;
      outline: 0;
      color: var(--ink);
      background: transparent;
      font-family: var(--mono);
      font-size: 13px;
    }
    .relay-submit {
      min-height: 42px;
      padding: 0 15px;
      border: 0;
      border-radius: 13px;
      corner-shape: squircle;
      color: #fff;
      background: var(--ink);
      cursor: pointer;
      font-weight: 660;
      transition: transform .32s var(--ease-spring), border-radius .35s var(--ease-morph);
    }
    .relay-submit:hover { transform: translateY(-2px); border-radius: 16px 10px 15px 11px; }

    .demo-wrap {
      opacity: 0;
      transform: translateY(26px) scale(.985);
      animation: demoIn 1s .34s var(--ease-out) forwards;
      perspective: 1200px;
    }

    .flow-demo {
      position: relative;
      min-height: 520px;
      border: 1px solid var(--line-strong);
      border-radius: 28px;
      corner-shape: squircle;
      background: rgba(255,255,251,.73);
      overflow: hidden;
      box-shadow: var(--shadow);
      transition: transform .45s var(--ease-spring), border-radius .5s var(--ease-morph), box-shadow .4s ease;
      transform-style: preserve-3d;
    }
    .flow-demo:hover { transform: translateY(-5px) rotateX(.3deg); border-radius: 34px 23px 32px 25px; box-shadow: 0 34px 100px rgba(18,19,16,.13); }

    .demo-head {
      height: 58px;
      display: grid;
      grid-template-columns: 1fr auto 1fr;
      align-items: center;
      padding: 0 18px;
      border-bottom: 1px solid var(--line);
    }
    .window-dots { display: flex; gap: 7px; }
    .window-dots span { width: 8px; height: 8px; border-radius: 50%; background: #d8d9d3; }
    .demo-title { color: var(--muted); font-family: var(--mono); font-size: 11px; letter-spacing: .045em; }
    .replay {
      justify-self: end;
      min-height: 34px;
      display: inline-flex;
      align-items: center;
      gap: 7px;
      padding: 0 10px;
      border: 0;
      border-radius: 11px;
      corner-shape: squircle;
      color: var(--muted);
      background: transparent;
      cursor: pointer;
      font-family: var(--mono);
      font-size: 10px;
      transition: color .2s ease, background .2s ease, transform .32s var(--ease-spring), border-radius .3s var(--ease-morph);
    }
    .replay:hover { color: var(--ink); background: rgba(23,24,21,.045); transform: translateY(-1px); border-radius: 14px 9px 13px 10px; }
    .replay svg { width: 14px; height: 14px; }
    .replay.running svg { animation: spin 1s var(--ease-out); }

    .terminal-grid {
      display: grid;
      grid-template-columns: 1fr 1fr;
      min-height: 354px;
    }
    .terminal-pane { min-width: 0; padding: 22px 24px 20px; }
    .terminal-pane + .terminal-pane { border-left: 1px solid var(--line); }
    .pane-head {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 14px;
      margin-bottom: 18px;
      color: var(--faint);
      font-family: var(--mono);
      font-size: 10px;
      letter-spacing: .07em;
      text-transform: uppercase;
    }
    .pane-head strong { color: var(--muted); font-weight: 650; }
    .terminal-lines {
      min-height: 244px;
      font-family: var(--mono);
      font-size: 12px;
      line-height: 1.72;
      letter-spacing: -.01em;
    }
    .terminal-line {
      min-height: 20px;
      color: var(--muted);
      opacity: 0;
      transform: translateY(5px);
      animation: terminalLine .3s var(--ease-out) forwards;
      overflow-wrap: anywhere;
    }
    .terminal-line.command { color: var(--ink-soft); }
    .terminal-line.command .prompt { color: var(--accent); font-weight: 760; }
    .terminal-line.success { color: var(--success); }
    .terminal-line.code { color: var(--ink); font-weight: 720; }
    .terminal-line.faint { color: var(--faint); }
    .caret {
      display: inline-block;
      width: 7px;
      height: 1.15em;
      margin-left: 2px;
      vertical-align: -.18em;
      background: var(--accent);
      animation: blink .72s steps(1) infinite;
    }

    .topology {
      min-height: 108px;
      display: grid;
      grid-template-columns: auto 1fr auto;
      align-items: center;
      gap: 18px;
      padding: 18px 24px;
      border-top: 1px solid var(--line);
      background: rgba(247,247,242,.55);
    }
    .node {
      min-width: 112px;
      display: flex;
      align-items: center;
      gap: 10px;
      color: var(--muted);
      font-family: var(--mono);
      font-size: 10px;
      transition: color .4s ease, transform .45s var(--ease-spring);
    }
    .node:last-child { justify-content: flex-end; text-align: right; }
    .node-icon {
      width: 34px;
      height: 34px;
      flex: 0 0 auto;
      display: grid;
      place-items: center;
      border: 1px solid var(--line-strong);
      border-radius: 12px;
      corner-shape: squircle;
      background: var(--surface-solid);
      transition: color .4s ease, background .4s ease, border-color .4s ease, transform .45s var(--ease-spring), border-radius .42s var(--ease-morph);
    }
    .node-icon svg { width: 16px; height: 16px; }
    .node small { display: block; margin-top: 3px; color: var(--faint); font-size: 9px; }
    .worker-meta {
      display: inline-flex;
      align-items: center;
      justify-content: flex-end;
      gap: 7px;
    }
    .add-node-hint {
      width: 19px;
      height: 19px;
      display: inline-grid;
      place-items: center;
      flex: 0 0 auto;
      border: 1px solid var(--line-strong);
      border-radius: 7px;
      corner-shape: squircle;
      color: var(--faint);
      background: rgba(255,255,251,.64);
      opacity: 0;
      transform: scale(.72) rotate(-10deg);
      transition: opacity .4s ease, transform .5s var(--ease-spring), color .25s ease, border-radius .35s var(--ease-morph);
    }
    .add-node-hint svg { width: 11px; height: 11px; }
    .flow-demo.online .add-node-hint { opacity: 1; transform: scale(1) rotate(0); }
    .node:last-child:hover .add-node-hint { color: var(--accent-deep); border-radius: 9px 6px 8px 7px; transform: scale(1.08) rotate(4deg); }
    .connection {
      position: relative;
      height: 34px;
      display: flex;
      align-items: center;
    }
    .connection::before {
      content: "";
      position: absolute;
      left: 0;
      right: 0;
      height: 1px;
      background: var(--line-strong);
    }
    .connection-fill {
      position: absolute;
      left: 0;
      width: 100%;
      height: 2px;
      border-radius: 99px;
      background: linear-gradient(90deg, var(--accent), var(--success));
      transform: scaleX(0);
      transform-origin: left;
      transition: transform 1s var(--ease-out);
    }
    .connection-label {
      position: relative;
      z-index: 1;
      margin: auto;
      padding: 6px 10px;
      color: var(--faint);
      background: #f8f8f3;
      border: 1px solid var(--line);
      border-radius: 10px;
      corner-shape: squircle;
      font-family: var(--mono);
      font-size: 9px;
      letter-spacing: .04em;
      transition: color .35s ease, border-color .35s ease, background .35s ease, border-radius .35s var(--ease-morph);
    }
    .flow-demo.approved .connection-fill { transform: scaleX(1); }
    .flow-demo.approved .connection-label { color: var(--success); border-color: rgba(35,129,90,.22); background: var(--success-soft); border-radius: 12px 8px 11px 9px; }
    .flow-demo.online .node:last-child { color: var(--ink); transform: translateY(-1px); }
    .flow-demo.online .node:last-child .node-icon { color: var(--success); background: var(--success-soft); border-color: rgba(35,129,90,.24); transform: scale(1.06); border-radius: 15px 10px 14px 11px; }

    footer {
      width: var(--content);
      min-height: 72px;
      margin: 0 auto;
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 24px;
      border-top: 1px solid var(--line);
      color: var(--faint);
      font-size: 12px;
      opacity: 0;
      animation: fadeIn .8s .8s ease forwards;
    }
    footer a { color: inherit; text-decoration: none; transition: color .2s ease; }
    footer a:hover { color: var(--ink-soft); }

    .toast {
      position: fixed;
      left: 50%;
      bottom: 24px;
      z-index: 20;
      min-height: 42px;
      display: flex;
      align-items: center;
      padding: 0 15px;
      color: #fff;
      background: var(--ink);
      border-radius: 14px;
      corner-shape: squircle;
      box-shadow: 0 18px 50px rgba(20,21,18,.2);
      font-size: 13px;
      font-weight: 620;
      opacity: 0;
      pointer-events: none;
      transform: translate(-50%, 16px) scale(.96);
      transition: opacity .28s ease, transform .42s var(--ease-spring), border-radius .35s var(--ease-morph);
    }
    .toast.show { opacity: 1; transform: translate(-50%, 0) scale(1); border-radius: 17px 11px 16px 12px; }

    @keyframes reveal { to { opacity: 1; transform: translateY(0); } }
    @keyframes fadeIn { to { opacity: 1; } }
    @keyframes lineIn { to { transform: scaleX(1); } }
    @keyframes demoIn { to { opacity: 1; transform: translateY(0) scale(1); } }
    @keyframes terminalLine { to { opacity: 1; transform: translateY(0); } }
    @keyframes blink { 50% { opacity: 0; } }
    @keyframes spin { to { transform: rotate(360deg); } }

    @media (max-width: 1180px) {
      main {
        grid-template-columns: 1fr;
        align-items: start;
        gap: 62px;
        padding-top: 72px;
      }
      .hero { max-width: 830px; }
      h1 { max-width: 850px; font-size: clamp(58px, 8.2vw, 92px); }
      .subhead { max-width: 720px; }
      .install-wrap { max-width: 720px; }
      .flow-demo { min-height: 500px; }
    }

    @media (max-width: 720px) {
      :root { --content: calc(100vw - 32px); }
      .topbar { height: 74px; }
      .brand { font-size: 14px; }
      .brand-mark { width: 32px; height: 32px; border-radius: 11px; }
      .github-link { min-height: 36px; padding: 0 11px; font-size: 13px; }
      main { padding: 54px 0 58px; gap: 46px; }
      h1 { font-size: clamp(48px, 13.5vw, 68px); letter-spacing: -.06em; }
      h1 .accent { width: auto; white-space: normal; }
      .subhead { margin-top: 22px; font-size: 17px; line-height: 1.52; }
      .actions { margin-top: 28px; flex-direction: column; align-items: stretch; gap: 12px; }
      .primary { justify-content: center; }
      .secondary { align-self: flex-start; }
      .install-wrap { margin-top: 18px; }
      .install-bar { min-height: 56px; border-radius: 16px; }
      .install-code { padding-left: 15px; font-size: 11px; }
      .copy-button { width: 52px; height: 54px; }
      .relay-form { grid-template-columns: 1fr auto; padding-left: 14px; }
      .relay-label { display: none; }
      .relay-input { font-size: 11px; }
      .relay-submit { padding: 0 12px; font-size: 12px; }
      .flow-demo { min-height: 0; border-radius: 20px; }
      .flow-demo:hover { transform: none; border-radius: 20px; }
      .demo-head { grid-template-columns: 1fr auto; height: 54px; padding: 0 13px; }
      .demo-title { display: none; }
      .terminal-grid { grid-template-columns: 1fr; min-height: 0; }
      .terminal-pane { padding: 20px 18px 18px; }
      .terminal-pane + .terminal-pane { border-left: 0; border-top: 1px solid var(--line); }
      .terminal-lines { min-height: 222px; font-size: 11px; }
      .topology { grid-template-columns: auto 1fr auto; gap: 9px; padding: 15px 14px; }
      .node { min-width: 0; font-size: 9px; }
      .node small { display: none; }
      .node-icon { width: 30px; height: 30px; border-radius: 10px; }
      .connection-label { padding: 5px 7px; font-size: 8px; }
      footer { min-height: 92px; align-items: flex-start; justify-content: center; flex-direction: column; gap: 7px; padding: 18px 0; }
    }

    @media (prefers-reduced-motion: reduce) {
      *, *::before, *::after { animation-duration: .001ms !important; animation-iteration-count: 1 !important; scroll-behavior: auto !important; transition-duration: .001ms !important; }
      .topbar, h1, .subhead, .actions, .install-wrap, .demo-wrap, footer { opacity: 1; transform: none; }
      h1 .accent::after { transform: scaleX(1); }
    }
  </style>
</head>
<body>
  <div class="noise" aria-hidden="true"></div>
  <div class="page">
    <header class="topbar">
      <a class="brand" href="#" aria-label="ContextBridge home">
        <span class="brand-mark" aria-hidden="true">
          <svg viewBox="0 0 128 128" role="img" aria-hidden="true">
            <rect width="128" height="128" rx="20" fill="#20231f"/>
            <path d="M31 36h27c14 0 23 8 23 20 0 7-3 13-9 16 9 3 14 11 14 21 0 15-11 24-28 24H31V36Zm26 32c8 0 12-4 12-11s-4-10-12-10H44v21h13Zm1 38c10 0 15-5 15-14 0-8-5-13-15-13H44v27h14Z" fill="#f6f6f1"/>
            <path d="M91 28h8v72h-8z" fill="#2ec4b6"/>
            <circle cx="95" cy="108" r="6" fill="#d7422f"/>
          </svg>
        </span>
        <span>ContextBridge</span>
      </a>

      <a class="github-link" href="https://github.com/IamAngusU/ContextBridge" target="_blank" rel="noreferrer" aria-label="Open ContextBridge on GitHub">
        <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="M12 2C6.48 2 2 6.58 2 12.24c0 4.52 2.87 8.35 6.84 9.7.5.1.68-.22.68-.49 0-.24-.01-1.05-.01-1.9-2.51.47-3.16-.63-3.36-1.21-.11-.28-.6-1.21-1.02-1.45-.35-.19-.85-.66-.01-.67.79-.01 1.35.74 1.54 1.05.9 1.55 2.34 1.11 2.91.84.09-.67.35-1.11.64-1.37-2.22-.26-4.55-1.14-4.55-5.06 0-1.12.39-2.04 1.03-2.76-.1-.26-.45-1.31.1-2.72 0 0 .84-.27 2.75 1.05A9.3 9.3 0 0 1 12 6.84a9.3 9.3 0 0 1 2.5.35c1.91-1.33 2.75-1.05 2.75-1.05.55 1.41.2 2.46.1 2.72.64.72 1.03 1.63 1.03 2.76 0 3.93-2.34 4.8-4.57 5.06.36.32.67.93.67 1.89 0 1.36-.01 2.46-.01 2.8 0 .27.18.59.69.49A10.26 10.26 0 0 0 22 12.24C22 6.58 17.52 2 12 2Z"/></svg>
        <span>GitHub</span>
        <span class="github-divider" aria-hidden="true"></span>
        <span class="github-stars" id="githubStarsWrap" data-loading="true" title="GitHub stars">
          <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path d="m12 2.7 2.83 5.73 6.32.92-4.58 4.46 1.08 6.29L12 17.13 6.35 20.1l1.08-6.29-4.58-4.46 6.32-.92L12 2.7Z"/></svg>
          <span id="githubStars" aria-label="Loading GitHub stars">···</span>
        </span>
      </a>
    </header>

    <main>
      <section class="hero" aria-labelledby="hero-title">
        <h1 id="hero-title"><span class="hero-line">Route AI jobs</span><span class="hero-line">across the compute</span><span class="hero-line accent">you control.</span></h1>
        <p class="subhead">Connect selected AI tabs, local models, private PCs, and servers through one relay—without opening worker ports.</p>

        <div class="actions">
          <a class="primary" href="https://github.com/IamAngusU/ContextBridge#quick-start" target="_blank" rel="noreferrer">
            <span>Get started</span>
            <svg viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="M5 12h14M14 7l5 5-5 5" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/></svg>
          </a>
          <button class="secondary" type="button" id="relayToggle" aria-expanded="false" aria-controls="relayDrawer">
            <svg viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="M8 7h8M8 12h8M8 17h5" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/><rect x="3" y="3" width="18" height="18" rx="4" stroke="currentColor" stroke-width="1.8"/></svg>
            <span>Open local dashboard</span>
          </button>
        </div>

        <div class="install-wrap">
          <div class="install-platforms" role="group" aria-label="Choose installer platform">
            <button class="install-platform" type="button" data-install-platform="unix" aria-pressed="true">Linux / macOS</button>
            <button class="install-platform" type="button" data-install-platform="windows" aria-pressed="false">Windows PowerShell</button>
          </div>
          <div class="install-bar" aria-label="Install command">
            <code class="install-code" id="installText">curl -fsSL https://angusu.de/contextbridge/install.sh | sh</code>
            <button class="copy-button" type="button" id="copyInstall" aria-label="Copy install command" title="Copy command">
              <svg viewBox="0 0 24 24" fill="none" aria-hidden="true"><rect x="8" y="8" width="11" height="11" rx="2" stroke="currentColor" stroke-width="1.7"/><path d="M16 8V6a2 2 0 0 0-2-2H6a2 2 0 0 0-2 2v8a2 2 0 0 0 2 2h2" stroke="currentColor" stroke-width="1.7"/></svg>
            </button>
          </div>
          <div class="install-meta" id="installMeta" aria-live="polite"><span id="releaseDownloads"></span><span id="uniqueInstallers"></span></div>

          <div class="relay-drawer" id="relayDrawer">
            <div class="relay-clip">
              <form class="relay-form" id="relayForm">
                <label class="relay-label" for="relayUrl">Dashboard</label>
                <input class="relay-input" id="relayUrl" name="relayUrl" type="url" value="http://127.0.0.1:32145" autocomplete="url" required />
                <button class="relay-submit" type="submit">Open</button>
              </form>
            </div>
          </div>
        </div>
      </section>

      <section class="demo-wrap" aria-label="Animated ContextBridge pairing flow">
        <div class="flow-demo" id="flowDemo">
          <div class="demo-head">
            <div class="window-dots" aria-hidden="true"><span></span><span></span><span></span></div>
            <div class="demo-title">relay + worker pairing</div>
            <button class="replay" id="replayDemo" type="button" aria-label="Replay pairing flow">
              <svg viewBox="0 0 24 24" fill="none" aria-hidden="true"><path d="M4 8V4m0 0h4M4 4l3.3 3.3a7 7 0 1 1-1.2 8.2" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg>
              <span>Replay</span>
            </button>
          </div>

          <div class="terminal-grid">
            <div class="terminal-pane">
              <div class="pane-head"><strong>Relay / VPS</strong><span>relay.example.com</span></div>
              <div class="terminal-lines" id="relayLines" aria-live="polite"></div>
            </div>
            <div class="terminal-pane">
              <div class="pane-head"><strong>Worker / local PC</strong><span>DEV-RIG</span></div>
              <div class="terminal-lines" id="workerLines" aria-live="polite"></div>
            </div>
          </div>

          <div class="topology" aria-label="Relay to worker connection state">
            <div class="node">
              <span class="node-icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none"><rect x="4" y="3" width="16" height="7" rx="2" stroke="currentColor" stroke-width="1.7"/><rect x="4" y="14" width="16" height="7" rx="2" stroke="currentColor" stroke-width="1.7"/><path d="M8 6.5h.01M8 17.5h.01" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"/></svg></span>
              <span>Relay<small>VPS · TLS</small></span>
            </div>
            <div class="connection">
              <span class="connection-fill" aria-hidden="true"></span>
              <span class="connection-label" id="connectionLabel">waiting for pairing</span>
            </div>
            <div class="node">
              <span>DEV-RIG<small class="worker-meta"><span id="slotStatus">RTX 3080 · 0 / 4 slots</span><span class="add-node-hint" title="Add another worker" aria-label="More workers supported"><svg viewBox="0 0 24 24" fill="none"><path d="M12 5v14M5 12h14" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"/></svg></span></small></span>
              <span class="node-icon" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none"><rect x="3" y="5" width="18" height="12" rx="2" stroke="currentColor" stroke-width="1.7"/><path d="M8 21h8M12 17v4" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"/></svg></span>
            </div>
          </div>
        </div>
      </section>
    </main>

    <footer>
      <span>ContextBridge · MIT licensed</span>
      <a href="https://angusu.de" target="_blank" rel="noreferrer">Powered by angusu.de</a>
    </footer>
  </div>

  <div class="toast" id="toast" role="status" aria-live="polite"></div>

  <script>
    (() => {
      const installCommands = {
        unix: 'curl -fsSL https://angusu.de/contextbridge/install.sh | sh',
        windows: 'irm https://angusu.de/contextbridge/install.ps1 | iex'
      };
      let installCommand = installCommands.unix;
      const installText = document.getElementById('installText');
      const installPlatforms = [...document.querySelectorAll('[data-install-platform]')];
      const copyButton = document.getElementById('copyInstall');
      const toast = document.getElementById('toast');
      let toastTimer = 0;

      function selectInstallPlatform(platform) {
        if (!Object.prototype.hasOwnProperty.call(installCommands, platform)) return;
        installCommand = installCommands[platform];
        installText.textContent = installCommand;
        for (const button of installPlatforms) {
          button.setAttribute('aria-pressed', button.dataset.installPlatform === platform ? 'true' : 'false');
        }
        copyButton.setAttribute('aria-label', `Copy ${platform === 'windows' ? 'Windows PowerShell' : 'Linux or macOS'} install command`);
      }

      for (const button of installPlatforms) {
        button.addEventListener('click', () => selectInstallPlatform(button.dataset.installPlatform));
      }
      selectInstallPlatform(/Windows/i.test(navigator.userAgent) ? 'windows' : 'unix');

      function showToast(message) {
        window.clearTimeout(toastTimer);
        toast.textContent = message;
        toast.classList.add('show');
        toastTimer = window.setTimeout(() => toast.classList.remove('show'), 1900);
      }

      copyButton.addEventListener('click', async () => {
        try {
          await navigator.clipboard.writeText(installCommand);
          showToast('Install command copied');
        } catch {
          const area = document.createElement('textarea');
          area.value = installCommand;
          area.style.position = 'fixed';
          area.style.opacity = '0';
          document.body.append(area);
          area.select();
          document.execCommand('copy');
          area.remove();
          showToast('Install command copied');
        }
      });

      const githubStars = document.getElementById('githubStars');
      const githubStarsWrap = document.getElementById('githubStarsWrap');
      const installMeta = document.getElementById('installMeta');
      const releaseDownloads = document.getElementById('releaseDownloads');
      const uniqueInstallers = document.getElementById('uniqueInstallers');
      const starCacheKey = 'contextbridge.github.stars';
      const formatStars = value => new Intl.NumberFormat('en', {
        notation: value >= 1000 ? 'compact' : 'standard',
        maximumFractionDigits: 1
      }).format(value);

      try {
        const cachedStars = Number.parseInt(localStorage.getItem(starCacheKey) || '', 10);
        if (Number.isFinite(cachedStars)) {
          githubStars.textContent = formatStars(cachedStars);
          githubStars.setAttribute('aria-label', `${cachedStars} GitHub stars`);
          githubStarsWrap.dataset.loading = 'false';
        }
      } catch {}

      fetch('/contextbridge/api/stats', { cache: 'no-store' })
        .then(response => {
          if (!response.ok) throw new Error(`Stats endpoint returned ${response.status}`);
          return response.json();
        })
        .then(stats => {
          const count = Number(stats.stars);
          if (!Number.isFinite(count)) return;
          githubStars.textContent = formatStars(count);
          githubStars.setAttribute('aria-label', `${count} GitHub stars`);
          githubStarsWrap.dataset.loading = 'false';
          try { localStorage.setItem(starCacheKey, String(count)); } catch {}
          const downloads = Number(stats.release_downloads || 0);
          const unique = Number(stats.unique_installers || 0);
          releaseDownloads.textContent = `${formatStars(downloads)} release asset download${downloads === 1 ? '' : 's'}`;
          uniqueInstallers.textContent = `${formatStars(unique)} installer network${unique === 1 ? '' : 's'} today`;
          installMeta.classList.add('ready');
        })
        .catch(() => {
          if (githubStarsWrap.dataset.loading === 'true') {
            githubStars.textContent = 'Star';
            githubStars.setAttribute('aria-label', 'Star ContextBridge on GitHub');
            githubStarsWrap.dataset.loading = 'false';
          }
        });

      const relayToggle = document.getElementById('relayToggle');
      const relayDrawer = document.getElementById('relayDrawer');
      const relayInput = document.getElementById('relayUrl');
      const relayForm = document.getElementById('relayForm');

      relayToggle.addEventListener('click', () => {
        const open = !relayDrawer.classList.contains('open');
        relayDrawer.classList.toggle('open', open);
        relayToggle.setAttribute('aria-expanded', String(open));
        if (open) window.setTimeout(() => relayInput.focus(), 280);
      });

      relayForm.addEventListener('submit', event => {
        event.preventDefault();
        let url;
        try { url = new URL(relayInput.value.trim()); }
        catch { showToast('Enter a valid dashboard URL'); relayInput.focus(); return; }
        if (!['http:', 'https:'].includes(url.protocol)) {
          showToast('Use an HTTP or HTTPS URL');
          relayInput.focus();
          return;
        }
        window.open(url.href, '_blank', 'noopener,noreferrer');
      });

      const demo = document.getElementById('flowDemo');
      const relayLines = document.getElementById('relayLines');
      const workerLines = document.getElementById('workerLines');
      const replay = document.getElementById('replayDemo');
      const connectionLabel = document.getElementById('connectionLabel');
      const slotStatus = document.getElementById('slotStatus');
      const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
      let sequenceId = 0;
      let loopTimer = 0;

      const sleep = ms => new Promise(resolve => window.setTimeout(resolve, reduceMotion ? 0 : ms));

      function line(container, html, className = '') {
        const row = document.createElement('div');
        row.className = `terminal-line ${className}`.trim();
        row.innerHTML = html;
        container.append(row);
        return row;
      }

      async function typeCommand(container, command, id) {
        const row = line(container, '<span class="prompt">$</span> ', 'command');
        const text = document.createElement('span');
        const caret = document.createElement('span');
        caret.className = 'caret';
        row.append(text, caret);
        if (reduceMotion) {
          text.textContent = command;
          caret.remove();
          return;
        }
        for (const char of command) {
          if (id !== sequenceId) return;
          text.textContent += char;
          await sleep(char === ' ' ? 17 : 24);
        }
        caret.remove();
        await sleep(120);
      }

      async function runDemo() {
        const id = ++sequenceId;
        window.clearTimeout(loopTimer);
        relayLines.innerHTML = '';
        workerLines.innerHTML = '';
        demo.classList.remove('approved', 'online');
        connectionLabel.textContent = 'waiting for pairing';
        slotStatus.textContent = 'RTX 3080 · 0 / 4 slots';
        replay.classList.add('running');
        await sleep(360);

        await Promise.all([
          (async () => {
            await typeCommand(relayLines, installCommand, id);
            if (id !== sequenceId) return;
            line(relayLines, 'Download checksum verified.', 'faint');
            line(relayLines, 'Device role: relay', 'code');
            line(relayLines, 'Public URL: https://relay.example.com', 'faint');
            line(relayLines, 'Cluster mode saved: relay', 'success');
            line(relayLines, 'ContextBridge user service enabled.', 'faint');
          })(),
          (async () => {
            await sleep(690);
            await typeCommand(workerLines, installCommand, id);
            if (id !== sequenceId) return;
            line(workerLines, 'Download checksum verified.', 'faint');
            line(workerLines, 'Device role: worker', 'code');
            line(workerLines, 'Relay URL: https://relay.example.com', 'faint');
            await sleep(250);
            line(workerLines, 'Pair this worker', 'faint');
            line(workerLines, 'Code: K7Q9-M2', 'code');
            line(workerLines, 'Open: https://relay.example.com/pair', 'faint');
            line(workerLines, 'Waiting for approval', 'faint');
          })()
        ]);

        if (id !== sequenceId) return;
        await sleep(650);
        await typeCommand(relayLines, 'contextbridge cluster pairing --approve K7Q9-M2', id);
        if (id !== sequenceId) return;
        line(relayLines, 'Worker DEV-RIG approved.', 'success');
        demo.classList.add('approved');
        connectionLabel.textContent = 'paired · outbound WSS';

        await sleep(620);
        if (id !== sequenceId) return;
        line(workerLines, '14:14:07  connected as DEV-RIG with 4 job slot(s)', 'success');
        demo.classList.add('online');
        connectionLabel.textContent = 'connected · outbound WSS';
        slotStatus.textContent = 'RTX 3080 · 1 / 4 slots running';
        replay.classList.remove('running');

        loopTimer = window.setTimeout(runDemo, reduceMotion ? 10000 : 5200);
      }

      replay.addEventListener('click', runDemo);
      runDemo();
    })();
  </script>
</body>
</html>
