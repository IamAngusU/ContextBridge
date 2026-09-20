<?php

// SPDX-License-Identifier: Apache-2.0

declare(strict_types=1);

require __DIR__ . '/ContextBridgeClient.php';

use ContextBridge\ContextBridgeClient;

function check(bool $condition, string $message): void
{
    if (!$condition) {
        throw new RuntimeException($message);
    }
}

final class ContextBridgePartialWriteStream
{
    public static string $bytes = '';

    /** @var resource|null Populated by PHP's stream wrapper runtime. */
    public $context;

    public function stream_open(string $path, string $mode, int $options, ?string &$openedPath): bool
    {
        self::$bytes = '';
        return true;
    }

    public function stream_write(string $data): int
    {
        $part = substr($data, 0, 3);
        self::$bytes .= $part;
        return strlen($part);
    }

    public function stream_flush(): bool
    {
        return true;
    }

    /** @return array<int,int> */
    public function stream_stat(): array
    {
        return [];
    }
}

$calls = [];
$responses = [
    ['status' => 503, 'headers' => [], 'body' => '{"error":"busy"}'],
    ['status' => 200, 'headers' => [], 'body' => '{"id":"job-1","status":"queued"}'],
    ['status' => 200, 'headers' => [], 'body' => '{"id":"job-1","status":"completed","result":{"output":{"mode":"text","text":"ok"}}}'],
];
$transport = static function (string $method, string $url, array $headers, ?string $body, int $timeout) use (&$calls, &$responses): array {
    $calls[] = compact('method', 'url', 'headers', 'body', 'timeout');
    return array_shift($responses) ?? ['status' => 500, 'headers' => [], 'body' => '{"error":"fixture exhausted"}'];
};
$client = new ContextBridgeClient('https://relay.example.test', 'cb_producer_test-token', 5, $transport);
$job = $client->job('job-1');
check(($job['status'] ?? '') === 'queued', 'safe GET did not retry once');
check(count($calls) === 2, 'safe GET retry count changed');
$job = $client->wait('job-1', 1, 100);
check(($job['status'] ?? '') === 'completed', 'wait did not observe completed job');

$notFoundCalls = 0;
$notFound = static function () use (&$notFoundCalls): array {
    $notFoundCalls++;
    return ['status' => 404, 'headers' => [], 'body' => '{"error":"not found"}'];
};
try {
    (new ContextBridgeClient('https://relay.example.test', 'cb_producer_test-token', 5, $notFound))->job('missing-job');
    throw new RuntimeException('missing job unexpectedly succeeded');
} catch (RuntimeException $error) {
    check($notFoundCalls === 1, 'non-transient safe read was retried');
}

$submitCalls = 0;
$rejecting = static function () use (&$submitCalls): array {
    $submitCalls++;
    return ['status' => 503, 'headers' => [], 'body' => '{"error":"busy"}'];
};
$submitClient = new ContextBridgeClient('https://relay.example.test', 'cb_producer_test-token', 5, $rejecting);
try {
    $submitClient->submit(['requirements' => [], 'payload' => ['prompt' => 'x']], 'record-1-v1');
    throw new RuntimeException('submit unexpectedly succeeded');
} catch (RuntimeException $error) {
    check($submitCalls === 1, 'mutating submit was silently retried');
}

$dir = sys_get_temp_dir() . DIRECTORY_SEPARATOR . 'contextbridge-php-' . bin2hex(random_bytes(6));
$data = "verified artifact\n";
$artifactJob = [
    'result' => [
        'output' => [
            'artifacts' => [[
                'name' => '../proof.txt',
                'media_type' => 'text/plain',
                'size' => strlen($data),
                'sha256' => hash('sha256', $data),
                'data_base64' => base64_encode($data),
            ]],
        ],
    ],
];
$saved = ContextBridgeClient::saveArtifacts($artifactJob, $dir);
check(count($saved['files']) === 1, 'artifact was not saved');
check(basename($saved['files'][0]['path']) === 'proof.txt', 'artifact path traversal was not removed');
check(file_get_contents($saved['files'][0]['path']) === $data, 'saved artifact bytes changed');
@unlink($saved['files'][0]['path']);
@rmdir($dir);

check(stream_wrapper_register('contextbridgepartial', ContextBridgePartialWriteStream::class), 'partial-write fixture could not be registered');
$partial = fopen('contextbridgepartial://artifact', 'wb');
check($partial !== false, 'partial-write fixture could not be opened');
$writeAll = new ReflectionMethod(ContextBridgeClient::class, 'writeAll');
$writeAll->invoke(null, $partial, 'partial-write-proof');
fclose($partial);
stream_wrapper_unregister('contextbridgepartial');
check(ContextBridgePartialWriteStream::$bytes === 'partial-write-proof', 'partial artifact writes were not completed');

try {
    new ContextBridgeClient('http://relay.example.test', 'cb_producer_test-token');
    throw new RuntimeException('plain remote HTTP unexpectedly accepted');
} catch (RuntimeException) {
}

echo "ContextBridge PHP client tests passed.\n";
