<?php

declare(strict_types=1);

namespace ContextBridge;

use RuntimeException;

/**
 * Small dependency-free client for ContextBridge's native cluster API.
 *
 * Keep producer tokens on the server. This client intentionally does not
 * retry POST or DELETE requests: an ambiguous external side effect needs an
 * operator decision. Exact submit retries are safe only when the caller
 * reuses the same Idempotency-Key and byte-equivalent request.
 */
final class ContextBridgeClient
{
    private const MAX_REQUEST_BYTES = 12 * 1024 * 1024;
    private const MAX_RESPONSE_BYTES = 24 * 1024 * 1024 + 128 * 1024;
    private const MAX_ARTIFACT_BYTES = 12 * 1024 * 1024;

    /** @var null|callable(string,string,array<string,string>,?string,int):array{status:int,headers:array<string,string>,body:string} */
    private $transport;

    /**
     * @param null|callable(string,string,array<string,string>,?string,int):array{status:int,headers:array<string,string>,body:string} $transport
     */
    public function __construct(
        private readonly string $relayUrl,
        private readonly string $producerToken,
        private readonly int $timeoutSeconds = 30,
        ?callable $transport = null,
    ) {
        $this->validateBaseUrl($relayUrl);
        if ($producerToken === '' || preg_match('/\s/', $producerToken) === 1) {
            throw new RuntimeException('Producer token is empty or contains whitespace.');
        }
        if ($timeoutSeconds < 1 || $timeoutSeconds > 300) {
            throw new RuntimeException('Timeout must be between 1 and 300 seconds.');
        }
        $this->transport = $transport;
    }

    /**
     * Admit one job. This method never retries automatically.
     *
     * @param array<string,mixed> $request
     * @return array<string,mixed>
     */
    public function submit(array $request, string $idempotencyKey): array
    {
        if (!$this->validIdempotencyKey($idempotencyKey)) {
            throw new RuntimeException('Idempotency key must contain 1-200 visible ASCII characters.');
        }
        $body = $this->encode($request);
        if (strlen($body) > self::MAX_REQUEST_BYTES) {
            throw new RuntimeException('Cluster request exceeds the 12 MiB cleartext limit.');
        }
        return $this->request(
            'POST',
            '/v1/cluster/jobs?compact=1',
            $body,
            ['Idempotency-Key' => $idempotencyKey],
            [200, 202],
            false,
        );
    }

    /** @return array<string,mixed> */
    public function job(string $jobId): array
    {
        $this->validateJobId($jobId);
        return $this->request(
            'GET',
            '/v1/cluster/jobs/' . rawurlencode($jobId) . '?compact=1',
            null,
            [],
            [200],
            true,
        );
    }

    /**
     * Wait for a durable terminal state. This polls only; it never resubmits.
     *
     * @return array<string,mixed>
     */
    public function wait(string $jobId, int $timeoutSeconds = 180, int $pollMilliseconds = 500): array
    {
        if ($timeoutSeconds < 1 || $timeoutSeconds > 3600) {
            throw new RuntimeException('Wait timeout must be between 1 and 3600 seconds.');
        }
        if ($pollMilliseconds < 100 || $pollMilliseconds > 10000) {
            throw new RuntimeException('Poll interval must be between 100 and 10000 milliseconds.');
        }
        $deadline = microtime(true) + $timeoutSeconds;
        do {
            $job = $this->job($jobId);
            $status = (string) ($job['status'] ?? '');
            if (in_array($status, ['completed', 'failed', 'cancelled'], true)) {
                return $job;
            }
            usleep($pollMilliseconds * 1000);
        } while (microtime(true) < $deadline);

        throw new RuntimeException('Timed out while waiting; the job was not resubmitted or cancelled.');
    }

    /**
     * Request durable cancellation once. Cancellation does not prove that an
     * already submitted provider action stopped before producing side effects.
     *
     * @return array<string,mixed>
     */
    public function cancel(string $jobId): array
    {
        $this->validateJobId($jobId);
        return $this->request(
            'DELETE',
            '/v1/cluster/jobs/' . rawurlencode($jobId),
            null,
            [],
            [200],
            false,
        );
    }

    /**
     * Decode and verify embedded artifacts from one completed compact job.
     * Existing files are never overwritten. URL-only references are returned
     * separately and are never fetched by this client.
     *
     * @param array<string,mixed> $job
     * @return array{files:list<array{name:string,path:string,size:int,sha256:string,media_type:string}>,references:list<string>}
     */
    public static function saveArtifacts(array $job, string $directory): array
    {
        $result = $job['result'] ?? null;
        if (is_string($result)) {
            $result = json_decode($result, true, 512, JSON_THROW_ON_ERROR);
        }
        $artifacts = is_array($result)
            ? ($result['output']['artifacts'] ?? [])
            : [];
        if (!is_array($artifacts)) {
            throw new RuntimeException('Result artifacts are malformed.');
        }

        if (!is_dir($directory) && !mkdir($directory, 0700, true) && !is_dir($directory)) {
            throw new RuntimeException('Could not create artifact directory.');
        }
        $root = realpath($directory);
        if ($root === false || is_link($directory)) {
            throw new RuntimeException('Artifact directory must resolve to a real directory.');
        }

        $files = [];
        $references = [];
        $total = 0;
        foreach ($artifacts as $artifact) {
            if (!is_array($artifact)) {
                throw new RuntimeException('Artifact entry is malformed.');
            }
            $encoded = (string) ($artifact['data_base64'] ?? '');
            $url = (string) ($artifact['url'] ?? '');
            if ($encoded === '') {
                if ($url !== '') {
                    $references[] = $url;
                }
                continue;
            }
            $data = base64_decode($encoded, true);
            if ($data === false || $data === '') {
                throw new RuntimeException('Artifact contains invalid or empty base64 data.');
            }
            $size = strlen($data);
            $total += $size;
            if ($total > self::MAX_ARTIFACT_BYTES) {
                throw new RuntimeException('Artifacts exceed the 12 MiB decoded limit.');
            }
            $declaredSize = $artifact['size'] ?? 0;
            if ($declaredSize !== 0 && (!is_int($declaredSize) || $declaredSize !== $size)) {
                throw new RuntimeException('Artifact size does not match its decoded bytes.');
            }
            $actualHash = hash('sha256', $data);
            $declaredHash = strtolower((string) ($artifact['sha256'] ?? ''));
            if ($declaredHash !== '' && (preg_match('/^[0-9a-f]{64}$/', $declaredHash) !== 1 || !hash_equals($declaredHash, $actualHash))) {
                throw new RuntimeException('Artifact failed SHA-256 verification.');
            }

            $name = self::safeFilename((string) ($artifact['name'] ?? 'artifact.bin'));
            [$path, $handle] = self::createExclusiveFile($root, $name);
            try {
                $written = fwrite($handle, $data);
                if ($written !== $size || !fflush($handle)) {
                    throw new RuntimeException('Could not write the complete artifact.');
                }
            } catch (\Throwable $error) {
                fclose($handle);
                @unlink($path);
                throw $error;
            }
            fclose($handle);
            @chmod($path, 0600);
            $files[] = [
                'name' => basename($path),
                'path' => $path,
                'size' => $size,
                'sha256' => $actualHash,
                'media_type' => (string) ($artifact['media_type'] ?? 'application/octet-stream'),
            ];
        }
        return ['files' => $files, 'references' => $references];
    }

    /**
     * @param array<string,string> $extraHeaders
     * @param list<int> $expectedStatuses
     * @return array<string,mixed>
     */
    private function request(
        string $method,
        string $path,
        ?string $body,
        array $extraHeaders,
        array $expectedStatuses,
        bool $safeRead,
    ): array {
        $attempts = $safeRead ? 4 : 1;
        $lastError = null;
        for ($attempt = 1; $attempt <= $attempts; $attempt++) {
            $headers = array_merge([
                'Authorization' => 'Bearer ' . $this->producerToken,
                'Accept' => 'application/json',
                'Content-Type' => 'application/json',
            ], $extraHeaders);
            try {
                $response = $this->send($method, $path, $headers, $body);
            } catch (RuntimeException $error) {
                $lastError = $error;
                if (!$safeRead || $attempt === $attempts) {
                    throw $error;
                }
                usleep(min(2000, 200 * (2 ** ($attempt - 1))) * 1000);
                continue;
            }
            if (in_array($response['status'], $expectedStatuses, true)) {
                $decoded = json_decode($response['body'], true, 512, JSON_THROW_ON_ERROR);
                if (!is_array($decoded)) {
                    throw new RuntimeException('Relay returned a non-object JSON response.');
                }
                return $decoded;
            }
            $lastError = new RuntimeException($this->httpError($response['status'], $response['body']));
            if (!$safeRead || !in_array($response['status'], [429, 502, 503, 504], true) || $attempt === $attempts) {
                throw $lastError;
            }
            usleep(min(2000, 200 * (2 ** ($attempt - 1))) * 1000);
        }
        throw $lastError ?? new RuntimeException('ContextBridge request failed.');
    }

    /** @param array<string,string> $headers @return array{status:int,headers:array<string,string>,body:string} */
    private function send(string $method, string $path, array $headers, ?string $body): array
    {
        if ($this->transport !== null) {
            return ($this->transport)($method, rtrim($this->relayUrl, '/') . $path, $headers, $body, $this->timeoutSeconds);
        }
        if (!function_exists('curl_init')) {
            throw new RuntimeException('The PHP cURL extension is required.');
        }
        $responseBody = '';
        $responseHeaders = [];
        $handle = curl_init(rtrim($this->relayUrl, '/') . $path);
        $headerLines = [];
        foreach ($headers as $name => $value) {
            $headerLines[] = $name . ': ' . $value;
        }
        curl_setopt_array($handle, [
            CURLOPT_CUSTOMREQUEST => $method,
            CURLOPT_HTTPHEADER => $headerLines,
            CURLOPT_CONNECTTIMEOUT => min(10, $this->timeoutSeconds),
            CURLOPT_TIMEOUT => $this->timeoutSeconds,
            CURLOPT_FOLLOWLOCATION => false,
            CURLOPT_MAXREDIRS => 0,
            CURLOPT_PROTOCOLS => CURLPROTO_HTTP | CURLPROTO_HTTPS,
            CURLOPT_REDIR_PROTOCOLS => 0,
            CURLOPT_SSL_VERIFYPEER => true,
            CURLOPT_SSL_VERIFYHOST => 2,
            CURLOPT_HEADERFUNCTION => static function ($curl, string $line) use (&$responseHeaders): int {
                $trimmed = trim($line);
                if ($trimmed !== '' && str_contains($trimmed, ':')) {
                    [$name, $value] = explode(':', $trimmed, 2);
                    $responseHeaders[strtolower(trim($name))] = trim($value);
                }
                return strlen($line);
            },
            CURLOPT_WRITEFUNCTION => static function ($curl, string $chunk) use (&$responseBody): int {
                if (strlen($responseBody) + strlen($chunk) > self::MAX_RESPONSE_BYTES) {
                    return 0;
                }
                $responseBody .= $chunk;
                return strlen($chunk);
            },
        ]);
        if ($body !== null) {
            curl_setopt($handle, CURLOPT_POSTFIELDS, $body);
        }
        $ok = curl_exec($handle);
        $status = (int) curl_getinfo($handle, CURLINFO_RESPONSE_CODE);
        $error = curl_error($handle);
        curl_close($handle);
        if ($ok === false) {
            throw new RuntimeException('ContextBridge transport failed' . ($error !== '' ? ': ' . $error : '.'));
        }
        return ['status' => $status, 'headers' => $responseHeaders, 'body' => $responseBody];
    }

    /** @param array<string,mixed> $value */
    private function encode(array $value): string
    {
        return json_encode($value, JSON_THROW_ON_ERROR | JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE);
    }

    private function validateBaseUrl(string $url): void
    {
        $parts = parse_url($url);
        if ($parts === false || isset($parts['user']) || isset($parts['pass']) || isset($parts['query']) || isset($parts['fragment']) || !isset($parts['scheme'], $parts['host'])) {
            throw new RuntimeException('Relay URL must be an absolute URL without credentials, query, or fragment.');
        }
        $scheme = strtolower((string) $parts['scheme']);
        $host = strtolower(rtrim((string) $parts['host'], '.'));
        $loopback = in_array($host, ['127.0.0.1', '::1', 'localhost'], true);
        if ($scheme !== 'https' && !($scheme === 'http' && $loopback)) {
            throw new RuntimeException('Relay URL must use HTTPS; plain HTTP is allowed only on loopback.');
        }
    }

    private function validateJobId(string $jobId): void
    {
        if (preg_match('/^[A-Za-z0-9_-](?:[A-Za-z0-9._-]{0,126}[A-Za-z0-9_-])?$/', $jobId) !== 1 || str_contains($jobId, '..')) {
            throw new RuntimeException('Invalid cluster job ID.');
        }
    }

    private function validIdempotencyKey(string $key): bool
    {
        return strlen($key) >= 1 && strlen($key) <= 200 && preg_match('/^[\x21-\x7e]+$/', $key) === 1;
    }

    private function httpError(int $status, string $body): string
    {
        $message = 'HTTP ' . $status;
        try {
            $decoded = json_decode($body, true, 32, JSON_THROW_ON_ERROR);
            if (is_array($decoded) && is_string($decoded['error'] ?? null)) {
                $message .= ': ' . substr($decoded['error'], 0, 500);
            }
        } catch (\Throwable) {
            // Never reflect arbitrary HTML or proxy bodies into logs.
        }
        return $message;
    }

    private static function safeFilename(string $name): string
    {
        $name = basename(str_replace('\\', '/', trim($name)));
        $name = preg_replace('/[\x00-\x1f<>:"\/\\|?*]+/', '-', $name) ?? '';
        $name = trim($name, " .\t\r\n");
        if ($name === '') {
            $name = 'artifact.bin';
        }
        return substr($name, 0, 180);
    }

    /** @return array{0:string,1:resource} */
    private static function createExclusiveFile(string $root, string $name): array
    {
        $extension = pathinfo($name, PATHINFO_EXTENSION);
        $suffix = $extension === '' ? '' : '.' . $extension;
        $stem = $extension === '' ? $name : substr($name, 0, -(strlen($extension) + 1));
        for ($index = 0; $index < 1000; $index++) {
            $candidate = $index === 0 ? $name : $stem . '-' . ($index + 1) . $suffix;
            $path = $root . DIRECTORY_SEPARATOR . $candidate;
            $handle = @fopen($path, 'x+b');
            if ($handle !== false) {
                return [$path, $handle];
            }
        }
        throw new RuntimeException('Could not choose a unique artifact filename.');
    }
}
