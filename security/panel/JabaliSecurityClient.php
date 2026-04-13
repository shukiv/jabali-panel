<?php

declare(strict_types=1);

namespace App\JabaliSecurity;

use Illuminate\Support\Facades\Http;
use Illuminate\Support\Facades\Log;

class JabaliSecurityClient
{
    protected string $baseUrl = 'http://localhost/api/v1';

    protected ?string $apiKey = null;

    protected ?string $socketPath = null;

    private static ?self $instance = null;

    public static function getInstance(): self
    {
        if (self::$instance === null) {
            self::$instance = new self;
        }

        return self::$instance;
    }

    public function __construct()
    {
        $this->loadConfig();
        // If no socket, fall back to TCP URL
        if (! $this->socketPath || ! file_exists($this->socketPath)) {
            $this->baseUrl = 'http://127.0.0.1:9876/api/v1';
            $this->socketPath = null;
        }
    }

    public function get(string $path, array $query = []): ?array
    {
        try {
            $response = $this->request()
                ->get($this->baseUrl.$path, $query);

            if ($response->successful()) {
                $data = $response->json();

                return $data['data'] ?? $data;
            }

            Log::error('JabaliSecurity API error: '.$response->status().' on GET '.$path);

            return null;
        } catch (\Exception $e) {
            Log::error('JabaliSecurity API unreachable: '.$e->getMessage());

            return null;
        }
    }

    public function post(string $path, array $data = []): ?array
    {
        try {
            $response = $this->request()
                ->post($this->baseUrl.$path, $data);

            if ($response->successful()) {
                $result = $response->json();

                return $result['data'] ?? $result;
            }

            Log::error('JabaliSecurity API error: '.$response->status().' on POST '.$path);

            return null;
        } catch (\Exception $e) {
            Log::error('JabaliSecurity API unreachable: '.$e->getMessage());

            return null;
        }
    }

    public function patch(string $path, array $data = []): ?array
    {
        try {
            $response = $this->request()
                ->patch($this->baseUrl.$path, $data);

            if ($response->successful()) {
                $result = $response->json();

                return $result['data'] ?? $result;
            }

            Log::error('JabaliSecurity API error: '.$response->status().' on PATCH '.$path);

            return null;
        } catch (\Exception $e) {
            Log::error('JabaliSecurity API unreachable: '.$e->getMessage());

            return null;
        }
    }

    public function delete(string $path): ?array
    {
        try {
            $response = $this->request()
                ->delete($this->baseUrl.$path);

            if ($response->successful()) {
                $result = $response->json();

                return $result['data'] ?? $result;
            }

            Log::error('JabaliSecurity API error: '.$response->status().' on DELETE '.$path);

            return null;
        } catch (\Exception $e) {
            Log::error('JabaliSecurity API unreachable: '.$e->getMessage());

            return null;
        }
    }

    public function isAvailable(): bool
    {
        try {
            $request = Http::withHeaders($this->headers())->timeout(3);
            if ($this->socketPath) {
                $request = $request->withOptions(['curl' => [CURLOPT_UNIX_SOCKET_PATH => $this->socketPath]]);
            }
            $response = $request->get($this->baseUrl.'/health');

            return $response->successful();
        } catch (\Exception) {
            return false;
        }
    }

    protected function request(): \Illuminate\Http\Client\PendingRequest
    {
        $request = Http::withHeaders($this->headers())->timeout(10);
        if ($this->socketPath) {
            $request = $request->withOptions(['curl' => [CURLOPT_UNIX_SOCKET_PATH => $this->socketPath]]);
        }

        return $request;
    }

    protected function headers(): array
    {
        $headers = ['Accept' => 'application/json'];
        if ($this->apiKey) {
            $headers['X-API-Key'] = $this->apiKey;
        }

        return $headers;
    }

    protected function loadConfig(): void
    {
        $configFile = '/etc/jabali-security/jabali-security.conf';
        if (! file_exists($configFile)) {
            return;
        }

        $content = file_get_contents($configFile);
        if ($content === false) {
            return;
        }

        if (preg_match('/^API_KEY="([^"]*)"$/m', $content, $matches)) {
            $this->apiKey = $matches[1] !== '' ? $matches[1] : null;
        }

        if (preg_match('/^API_SOCKET="([^"]*)"$/m', $content, $matches)) {
            $this->socketPath = $matches[1] !== '' ? $matches[1] : null;
        } else {
            $this->socketPath = '/run/jabali-security/jabali-security.sock';
        }
    }
}
