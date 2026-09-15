<?php
declare(strict_types=1);

if (!class_exists('SQLite3')) {
    fwrite(STDERR, "SQLite3 extension is required\n");
    exit(1);
}

$testDirectory = sys_get_temp_dir() . DIRECTORY_SEPARATOR . 'contextbridge-metrics-' . bin2hex(random_bytes(8));
if (!mkdir($testDirectory, 0700, true) && !is_dir($testDirectory)) {
    throw new RuntimeException('could not create test directory');
}
register_shutdown_function(static function () use ($testDirectory): void {
    foreach (['downloads.sqlite', 'downloads.sqlite-shm', 'downloads.sqlite-wal', 'fingerprint-secret'] as $name) {
        @unlink($testDirectory . DIRECTORY_SEPARATOR . $name);
    }
    @rmdir($testDirectory);
});

putenv('CONTEXTBRIDGE_SITE_DATA=' . $testDirectory);
define('CONTEXTBRIDGE_LIBRARY_ONLY', true);
require dirname(__DIR__) . '/index.php';

function expectMetric(bool $condition, string $message): void
{
    if (!$condition) {
        throw new RuntimeException($message);
    }
}

$day = strtotime('2026-09-15 12:00:00 UTC');
$_SERVER['REMOTE_ADDR'] = '203.0.113.7';
$_SERVER['HTTP_USER_AGENT'] = 'curl/8.0 variant-one';
$fingerprint = clientFingerprint(gmdate('Y-m-d', $day));
$_SERVER['HTTP_USER_AGENT'] = str_repeat('attacker-controlled-', 200);
expectMetric(clientFingerprint(gmdate('Y-m-d', $day)) === $fingerprint, 'user-agent variants changed metric identity');
$_SERVER['REMOTE_ADDR'] = '203.0.113.200';
expectMetric(clientFingerprint(gmdate('Y-m-d', $day)) === $fingerprint, 'same IPv4 /24 did not share a privacy-preserving identity');

$_SERVER['REMOTE_ADDR'] = '203.0.113.7';
for ($index = 0; $index < 32; $index++) {
    recordDownload('install.sh', $day);
}
$stats = localDownloadStatistics($day);
expectMetric($stats === ['total' => CONTEXTBRIDGE_METRIC_REQUESTS_PER_CLIENT_DAY, 'unique' => 1], 'per-client/day metric bound failed: ' . json_encode($stats));

$_SERVER['REMOTE_ADDR'] = '198.51.100.9';
recordDownload('install.sh', $day);
$stats = localDownloadStatistics($day);
expectMetric($stats === ['total' => CONTEXTBRIDGE_METRIC_REQUESTS_PER_CLIENT_DAY + 1, 'unique' => 2], 'second network metric failed: ' . json_encode($stats));
recordDownload('install.ps1', $day);
$stats = localDownloadStatistics($day);
expectMetric($stats === ['total' => CONTEXTBRIDGE_METRIC_REQUESTS_PER_CLIENT_DAY + 2, 'unique' => 2], 'same network was double-counted across installer kinds: ' . json_encode($stats));

$_SERVER['REMOTE_ADDR'] = '203.0.113.7';
$nextDay = $day + 86400;
expectMetric(clientFingerprint(gmdate('Y-m-d', $nextDay)) !== $fingerprint, 'fingerprint did not rotate at the UTC-day boundary');
for ($index = 0; $index < 32; $index++) {
    recordDownload('install.sh', $nextDay);
}
$stats = localDownloadStatistics($nextDay);
expectMetric($stats === ['total' => (2 * CONTEXTBRIDGE_METRIC_REQUESTS_PER_CLIENT_DAY) + 2, 'unique' => 1], 'rotated daily window failed: ' . json_encode($stats));

$afterRetention = $day + ((CONTEXTBRIDGE_METRIC_RETENTION_DAYS + 2) * 86400);
recordDownload('install.sh', $afterRetention);
$database = siteDatabase();
$remainingWindows = (int) $database->querySingle('SELECT COUNT(*) FROM download_unique_windows');
$database->exec('CREATE TABLE download_uniques (kind TEXT, fingerprint TEXT, first_seen TEXT)');
$database->close();
$database = siteDatabase();
$legacyTable = (int) $database->querySingle("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'download_uniques'");
$database->close();
expectMetric($remainingWindows === 1, 'expired metric windows were not pruned');
expectMetric($legacyTable === 0, 'legacy permanent fingerprint table was retained');

$beforeInvalid = localDownloadStatistics($afterRetention);
recordDownload('attacker-selected-kind', $afterRetention);
expectMetric(localDownloadStatistics($afterRetention) === $beforeInvalid, 'unknown installer kind changed metrics');

fwrite(STDOUT, "installer metrics: ok\n");
