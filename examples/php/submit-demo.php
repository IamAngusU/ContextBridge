<?php

// SPDX-License-Identifier: Apache-2.0

declare(strict_types=1);

require __DIR__ . '/ContextBridgeClient.php';

use ContextBridge\ContextBridgeClient;

$relay = getenv('CONTEXTBRIDGE_RELAY_URL') ?: '';
$token = getenv('CONTEXTBRIDGE_PRODUCER_TOKEN') ?: '';
$key = getenv('CONTEXTBRIDGE_IDEMPOTENCY_KEY') ?: '';
if ($relay === '' || $token === '' || $key === '') {
    fwrite(STDERR, "Set CONTEXTBRIDGE_RELAY_URL, CONTEXTBRIDGE_PRODUCER_TOKEN, and a stable CONTEXTBRIDGE_IDEMPOTENCY_KEY.\n");
    exit(2);
}

$client = new ContextBridgeClient($relay, $token);
$request = [
    'source' => 'php-shared-hosting-demo',
    'requirements' => [
        'task' => 'generation',
        'provider' => 'ollama',
        'model' => 'qwen2.5:latest',
    ],
    'payload' => [
        'source' => 'php-shared-hosting-demo',
        'route' => 'modelkit',
        'provider' => 'ollama',
        'model' => 'qwen2.5:latest',
        'prompt' => 'Reply exactly with PHP-POOL-OK and nothing else.',
        'output' => ['mode' => 'text', 'max_bytes' => 4096],
    ],
    'max_attempts' => 1,
];

$admitted = $client->submit($request, $key);
$job = $client->wait((string) $admitted['id']);
if (($job['status'] ?? '') !== 'completed') {
    fwrite(STDERR, 'Job ended as ' . ($job['status'] ?? 'unknown') . ': ' . ($job['error'] ?? 'no reason') . "\n");
    exit(1);
}

echo json_encode($job['result'] ?? null, JSON_PRETTY_PRINT | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE), "\n";
